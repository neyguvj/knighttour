package halfprof

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/graph"
	"knighttour/pairs"
)

// TestGroupsOrbits pins orbit bookkeeping: rep count, Σ K = C(n²,2), order
// and S < T invariants.
func TestGroupsOrbits(t *testing.T) {
	cases := []struct {
		n      int
		groups int
	}{
		{5, 49},
		{6, 93},
		{7, 171},
		{8, 278},
	}
	for _, tc := range cases {
		gs := Groups(tc.n)
		assert.Len(t, gs, tc.groups, "n=%d", tc.n)
		var pairs int
		prev := -1
		for _, g := range gs {
			assert.Less(t, g.S, g.T, "n=%d rep order", tc.n)
			assert.Contains(t, []int{2, 4, 8}, g.K, "n=%d orbit size", tc.n)
			code := g.S*tc.n*tc.n + g.T
			assert.Greater(t, code, prev, "n=%d sorted", tc.n)
			prev = code
			pairs += g.K
		}
		assert.Equal(t, tc.n*tc.n*(tc.n*tc.n-1)/2, pairs, "n=%d orbit cover", tc.n)
	}
	assert.Nil(t, Groups(4))
	assert.Nil(t, Groups(9))
}

// TestSinglePathOK table-tests the alternating-links walk over pairings of
// m occurrences plus zero ends ⊥s=m and ⊥t=m+1.
func TestSinglePathOK(t *testing.T) {
	cases := []struct {
		name string
		p, q []int8
		m    int
		want bool
	}{
		{"chain s-0-1-t", []int8{1, 0, -1, -1}, []int8{2, 3, 0, 1}, 2, true},
		{"direct s-t", []int8{-1, -1}, []int8{1, 0}, 0, true},
		{"internal cycle", []int8{1, 0, -1, -1}, []int8{1, 0, 3, 2}, 2, false},
		{"broken coverage", []int8{-1, -1, 3, 2}, []int8{1, 0, -1, -1}, 2, false},
		{"wrong end", []int8{2, -1, 0, -1}, []int8{-1, -1, -1, -1}, 2, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, singlePathOK(tc.p, tc.q, tc.m), tc.name)
	}
}

// TestCountGroupInvalid checks the contract errors.
func TestCountGroupInvalid(t *testing.T) {
	ctx := context.Background()
	_, err := CountGroup(ctx, 4, 0, 1)
	require.Error(t, err)
	_, err = CountGroup(ctx, 9, 0, 1)
	require.Error(t, err)
	_, err = CountGroup(ctx, 5, 24, 25)
	require.Error(t, err)
	_, err = CountGroup(ctx, 5, 3, 3)
	require.Error(t, err)
}

// TestTerminatedContext: a dead ctx aborts with ctx.Err() and no panic.
func TestTerminatedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CountGroup(ctx, 5, 0, 6)
	require.ErrorIs(t, err, context.Canceled)
	_, err = CountBoard(ctx, 5, 2)
	require.ErrorIs(t, err, context.Canceled)
}

// TestOverCap lowers the soft cap to a token size and pins the sentinel.
func TestOverCap(t *testing.T) {
	old := softCap
	softCap = 1024
	defer func() { softCap = old }()
	_, err := CountGroup(context.Background(), 6, 0, 8)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOverCap)
	var oce *OverCapError
	require.ErrorAs(t, err, &oce)
	assert.NotEmpty(t, oce.Side)
}

// TestDeterminismAndWorkers: repeated runs and different crews agree.
func TestDeterminismAndWorkers(t *testing.T) {
	first, err := CountGroup(context.Background(), 6, 0, 8)
	require.NoError(t, err)
	again, err := CountGroup(context.Background(), 6, 0, 8)
	require.NoError(t, err)
	assert.Equal(t, first, again)

	t1, err := CountBoard(context.Background(), 5, 1)
	require.NoError(t, err)
	t2, err := CountBoard(context.Background(), 5, 2)
	require.NoError(t, err)
	assert.Equal(t, t1, t2)
	assert.Equal(t, uint64(1728), t1)
}

// TestPairsOracle5x5 cross-checks every ordered endpoint pair of 5×5 against
// the independent pairs engine: CountGroup (undirected edge sets between
// {s,t}) equals the directed path count N(s,t) cell for cell.
func TestPairsOracle5x5(t *testing.T) {
	g := graph.New(5)
	m := pairs.Compute(context.Background(), g, 4)
	for s := range 25 {
		for tt := range 25 {
			if s == tt {
				continue
			}
			hp, err := CountGroup(context.Background(), 5, s, tt)
			require.NoError(t, err)
			assert.Equal(t, m.At(s, tt), hp, "pair %d,%d", s, tt)
		}
	}
}

// TestCensusCounters: statistics are populated and consistent for one group.
func TestCensusCounters(t *testing.T) {
	total, cen, err := CountGroupWithCensus(context.Background(), 5, 0, 6)
	require.NoError(t, err)
	assert.Positive(t, cen.ProfilesU)
	assert.Positive(t, cen.ProfilesL)
	assert.NotZero(t, cen.Joins)
	assert.Positive(t, cen.MaxProfileCount)
	assert.LessOrEqual(t, cen.MaxProfileCount, total)
}

// fuseCtx reports cancellation from its N-th Err call on — a deterministic
// seam for testing "cancelled exactly during this phase".
type fuseCtx struct {
	context.Context
	limit int
	calls int
}

// Err arms the cancellation once the call budget is spent.
func (f *fuseCtx) Err() error {
	f.calls++
	if f.calls > f.limit {
		return context.Canceled
	}
	return f.Context.Err()
}

// TestJoinPhaseCancellation: tables enumerated with a live ctx, then the join
// sees cancellation — mid-merge via the fuse and outright with a dead ctx.
// Both must return context.Canceled without panicking (specs/halfprof.md).
func TestJoinPhaseCancellation(t *testing.T) {
	g := newCutGeom(5)
	bud := &memBudget{}
	up, lo := newAggTable("upper", bud), newAggTable("lower", bud)
	require.NoError(t, runHalf(g, context.Background(), false, 0, 6, 0, 1, up))
	require.NoError(t, runHalf(g, context.Background(), true, 0, 6, 0, 1, lo))
	ue, le := sortedEntries(up.m), sortedEntries(lo.m)

	live, joins, err := joinTables(context.Background(), ue, le)
	require.NoError(t, err)
	assert.EqualValues(t, 16, live)
	assert.NotZero(t, joins)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = joinTables(ctx, ue, le)
	require.ErrorIs(t, err, context.Canceled)

	fuse := &fuseCtx{Context: context.Background(), limit: 3}
	_, _, err = joinTables(fuse, ue, le)
	require.ErrorIs(t, err, context.Canceled)
	assert.Greater(t, fuse.calls, 3, "the join must poll ctx repeatedly across runs")
}
