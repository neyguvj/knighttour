package halfprof

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dp_test.go — the profile-DP engine (plan 31 step 0) must reproduce the
// enumerator's tables bit-for-bit: same profiles, same weights. Equivalence
// runs on every non-SKIP group of 5×5 and a sampled set of 6×6 groups; the
// join of DP tables must give the counted total through the untouched merge.

// enumHalfTable enumerates one half with the reference backtracking engine.
func enumHalfTable(t *testing.T, g *cutGeom, lower bool, s, tt int) map[profKey]uint64 {
	t.Helper()
	tab := newAggTable("enum", &memBudget{})
	require.NoError(t, runHalf(g, context.Background(), lower, s, tt, 0, 1, tab))
	return tab.m
}

// dpHalfTable runs one half with the profile-DP engine.
func dpHalfTable(t *testing.T, g *cutGeom, lower bool, s, tt int) (map[profKey]uint64, DpCensus) {
	t.Helper()
	tab := newAggTable("dp", &memBudget{})
	cen, err := runHalfDp(g, context.Background(), lower, s, tt, 0, 1, &memBudget{}, tab)
	require.NoError(t, err)
	return tab.m, cen
}

// TestDpEqualsEnumerator5x5 pins the DP half tables against the enumerator on
// every structurally non-zero group of 5×5 (the make-check cheap board).
func TestDpEqualsEnumerator5x5(t *testing.T) {
	for _, gr := range Groups(5) {
		if structurallyZeroGroup(5, gr.S, gr.T) {
			continue
		}
		t.Run(groupName(gr), func(t *testing.T) {
			t.Parallel()
			g := newCutGeom(5)
			for _, lower := range []bool{false, true} {
				want := enumHalfTable(t, g, lower, gr.S, gr.T)
				got, cen := dpHalfTable(t, g, lower, gr.S, gr.T)
				assert.Equal(t, want, got, "lower=%v", lower)
				// The upper half derives its terminals from cells (canonical), so
				// final states are exactly the profiles; the lower half keys keep
				// chosen crossing identities for the byte-exact profile encoding,
				// so several equivalent-structure states may share one profile.
				if lower {
					assert.GreaterOrEqual(t, cen.FinalStates, len(want), "lower=%v", lower)
				} else {
					assert.Equal(t, len(want), cen.FinalStates, "lower=%v", lower)
				}
			}
		})
	}
}

// TestDpEqualsEnumerator6x6 samples 6×6 against the enumerator within the
// unit budget (plan-28 rule: only points up to ~60 s under -race live here,
// the full 45-group sweep and the heavy class-A lower halves are bench work —
// specs/benchmarks.md «Профиль-DP ценз половин»): the class-A upper half and
// one light cross-row group per half.
func TestDpEqualsEnumerator6x6(t *testing.T) {
	cases := []struct {
		name  string
		s, t  int
		lower bool
	}{
		{"g0-1/up", 0, 1, false},   // class A upper (profilesU 16 913)
		{"g8-26/up", 8, 26, false}, // light cross-row5 tail
		{"g8-26/lo", 8, 26, true},  // its lower half (profilesL 32 466)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newCutGeom(6)
			want := enumHalfTable(t, g, tc.lower, tc.s, tc.t)
			got, cen := dpHalfTable(t, g, tc.lower, tc.s, tc.t)
			assert.Equal(t, want, got)
			assert.GreaterOrEqual(t, cen.FinalStates, len(want))
		})
	}
}

// TestDpJoinCount5x5 counts 5×5 groups with DP tables through the untouched
// join and compares against the reference pipeline (itself pinned by the
// pairs oracle in halfprof_test.go): a sampled set covering the heaviest
// table, a zero-tour group and one plain point.
func TestDpJoinCount5x5(t *testing.T) {
	cases := []struct{ s, t int }{{0, 2}, {0, 24}, {2, 22}}
	for _, tc := range cases {
		gr := Group{S: tc.s, T: tc.t}
		t.Run(groupName(gr), func(t *testing.T) {
			t.Parallel()
			g := newCutGeom(5)
			upTab := newAggTable("upper", &memBudget{})
			cenU, err := runHalfDp(g, context.Background(), false, gr.S, gr.T, 0, 1, &memBudget{}, upTab)
			require.NoError(t, err)
			loTab := newAggTable("lower", &memBudget{})
			cenL, err := runHalfDp(g, context.Background(), true, gr.S, gr.T, 0, 1, &memBudget{}, loTab)
			require.NoError(t, err)

			total, _, err := joinTables(context.Background(), sortedEntries(upTab.m), sortedEntries(loTab.m))
			require.NoError(t, err)
			want, err := CountGroup(context.Background(), 5, gr.S, gr.T)
			require.NoError(t, err)
			assert.Equal(t, want, total)

			// state weights are forest counts (they may exceed a zero-tour
			// group's total), but both halves must have produced states.
			assert.Positive(t, cenU.LayerPeak)
			assert.Positive(t, cenL.LayerPeak)
		})
	}
}

// TestDpBucketFilter: the DP emission keeps exactly the masks of its bucket,
// so summing passes reproduces the single-pass table (enumerator semantics).
func TestDpBucketFilter(t *testing.T) {
	g := newCutGeom(5)
	const s, tt = 0, 6
	want := enumHalfTable(t, g, true, s, tt)
	merged := make(map[profKey]uint64)
	for pass := range 3 {
		tab := newAggTable("dp", &memBudget{})
		_, err := runHalfDp(g, context.Background(), true, s, tt, pass, 3, &memBudget{}, tab)
		require.NoError(t, err)
		for k, w := range tab.m {
			assert.Equal(t, pass, maskBucket(k.K, 3), "profile %v in wrong bucket", k.K)
			merged[k] += w
		}
	}
	assert.Equal(t, want, merged)
}

// TestDpDeterminism: two runs of both halves produce bitwise-equal tables and
// identical census counters (the layers and transitions are fixed order).
func TestDpDeterminism(t *testing.T) {
	g := newCutGeom(5)
	for _, lower := range []bool{false, true} {
		firstTab := newAggTable("dp", &memBudget{})
		first, err := runHalfDp(g, context.Background(), lower, 0, 24, 0, 1, &memBudget{}, firstTab)
		require.NoError(t, err)
		secondTab := newAggTable("dp", &memBudget{})
		second, err := runHalfDp(g, context.Background(), lower, 0, 24, 0, 1, &memBudget{}, secondTab)
		require.NoError(t, err)
		assert.Equal(t, firstTab.m, secondTab.m, "lower=%v", lower)
		assert.Equal(t, first.Layers, second.Layers, "lower=%v", lower)
		assert.Equal(t, first.MaxWeight, second.MaxWeight, "lower=%v", lower)
	}
}

// TestDpTerminatedContext: a dead ctx aborts both halves with ctx.Err().
func TestDpTerminatedContext(t *testing.T) {
	g := newCutGeom(5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, lower := range []bool{false, true} {
		_, err := runHalfDp(g, ctx, lower, 0, 6, 0, 1, &memBudget{}, newAggTable("dp", &memBudget{}))
		require.ErrorIs(t, err, context.Canceled)
	}
}

// TestDpMidRunCancellation: a fuse ctx armed after a few polls cancels the
// walk in progress (not just at entry).
func TestDpMidRunCancellation(t *testing.T) {
	g := newCutGeom(6)
	fuse := &fuseCtx{Context: context.Background(), limit: 4}
	_, err := runHalfDp(g, fuse, true, 0, 1, 0, 1, &memBudget{}, newAggTable("dp", &memBudget{}))
	require.ErrorIs(t, err, context.Canceled)
	assert.Greater(t, fuse.calls, 4, "the walk must poll ctx across layers")
}

// TestDpSoftCap lowers the cap to a token size: the run aborts with the DP
// over-cap error carrying cutoff progress, as ErrOverCap.
func TestDpSoftCap(t *testing.T) {
	old := softCap
	softCap = 1 << 20 // fits the seed tables, far below a full 6×6 half
	defer func() { softCap = old }()

	g := newCutGeom(6)
	cen, err := runHalfDp(g, context.Background(), true, 0, 1, 0, 1, &memBudget{}, newAggTable("dp", &memBudget{}))
	require.ErrorIs(t, err, ErrOverCap)
	var oce *DpOverCapError
	require.ErrorAs(t, err, &oce)
	assert.Equal(t, "lower", oce.Side)
	assert.Positive(t, oce.States)
	assert.NotEmpty(t, cen.Layers, "layers published before the cutoff stay in the census")
}

// groupName renders a group subtest label as in the census harness.
func groupName(gr Group) string {
	return fmt.Sprintf("g%d-%d", gr.S, gr.T)
}
