package shapecount_test

import (
	"math/bits"
	"math/rand"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/graph"
	"knighttour/pruner"
	"knighttour/shapecount"
	"knighttour/state"
	"knighttour/types"
)

// bruteForceH counts knight paths covering exactly mask and ending at end by
// plain reverse DFS without memoization — the reference implementation.
func bruteForceH(g *graph.Graph, mask state.State, end int) uint64 {
	if !mask.IsVisited(end) {
		return 0
	}
	var walk func(cur int, todo state.State) uint64
	walk = func(cur int, todo state.State) uint64 {
		if todo == 0 {
			return 1
		}
		var n uint64
		for c := range g.GetNeighborMask(cur).Intersect(todo).AllVisited() {
			n += walk(c, todo.Unvisit(c))
		}
		return n
	}
	return walk(end, mask.Unvisit(end))
}

func randomWalkMask(rng *rand.Rand, g *graph.Graph, length int) (state.State, bool) {
	for range 100 {
		mask := state.Bit(rng.Intn(g.GetTotalCells()))
		cur := bits.TrailingZeros64(uint64(mask))
		grew := true
		for range length - 1 {
			var cand []int
			for n := range g.GetNeighborMask(cur).AllVisited() {
				if mask.IsUnvisited(n) {
					cand = append(cand, n)
				}
			}
			if len(cand) == 0 {
				grew = false
				break
			}
			cur = cand[rng.Intn(len(cand))]
			mask = mask.Visit(cur)
		}
		if grew && mask.CountBits() == length {
			return mask, true
		}
	}
	return 0, false
}

func TestCountShapeExhaustiveSmall(t *testing.T) {
	const size = 5
	g := graph.New(size)
	sc := shapecount.New(g)

	cells := state.State(1 << (size * size))
	for m := state.State(1); m < cells; m++ {
		if bits.OnesCount64(uint64(m)) > 5 {
			continue
		}
		var ends []int
		for e := range m.AllVisited() {
			ends = append(ends, e)
		}
		got := sc.CountShape(m, ends, nil)
		for i, e := range ends {
			assert.Equalf(t, bruteForceH(g, m, e), got[i], "mask=%b end=%d", uint64(m), e)
		}
	}
}

func TestCountShapeRandomConnected(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test seed

	for _, size := range []int{5, 6} {
		g := graph.New(size)
		sc := shapecount.New(g)

		for trial := range 300 {
			mask, ok := randomWalkMask(rng, g, 6+rng.Intn(9)) // sizes 6..14
			if !ok {
				continue
			}
			var ends []int
			for e := range mask.AllVisited() {
				ends = append(ends, e)
			}
			got := sc.CountShape(mask, ends, nil)
			for i, e := range ends {
				require.Equalf(t, bruteForceH(g, mask, e), got[i],
					"size=%d trial=%d mask=%b end=%d", size, trial, uint64(mask), e)
			}
		}
	}
}

func TestCountShapeZerosAndForeignEnd(t *testing.T) {
	g := graph.New(8)
	sc := shapecount.New(g)

	// Two cells far apart: no knight path covers both.
	mask := state.NewState(0, 7)
	got := sc.CountShape(mask, []int{0, 7}, nil)
	assert.Equal(t, []uint64{0, 0}, got)

	// End outside the mask is zero as well.
	got = sc.CountShape(mask, []int{3, 0}, nil)
	require.Len(t, got, 2)
	assert.Zero(t, got[0])

	// A single cell has exactly one covering path.
	assert.Equal(t, []uint64{1}, sc.CountShape(state.NewState(0), []int{0}, nil))
}

func TestCountShapePruneStats(t *testing.T) {
	g := graph.New(8)
	sc := shapecount.New(g)

	tests := []struct {
		name   string
		ends   []int
		wantHs []uint64
		want   types.Result
		mask   state.State
	}{
		{
			name: "no pruning",
			mask: state.NewState(0), ends: []int{0},
			wantHs: []uint64{1},
		},
		{
			// Two far-apart cells: every descent dies on the lone unreachable cell.
			name: "dead end per end",
			mask: state.NewState(0, 7), ends: []int{0, 7},
			wantHs: []uint64{0, 0},
			want:   types.Result{Pruned: 2, PrunedDeadEnd: 2, DPStates: 2},
		},
		{
			// Star 10-0-17: end 0 dies immediately (leaves unreachable), end 10 walks fine.
			name: "pruning with nonzero result",
			mask: state.NewState(0, 10, 17), ends: []int{0, 10},
			wantHs: []uint64{0, 1},
			want:   types.Result{Pruned: 1, PrunedDeadEnd: 1, DPStates: 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var res types.Result
			got := sc.CountShape(tt.mask, tt.ends, &res)
			assert.Equal(t, tt.wantHs, got)
			assert.Equal(t, tt.want, res)
			assert.Equal(t,
				res.PrunedDeadEnd+res.PrunedNoCont+res.PrunedDisconn+res.PrunedEndpoints,
				res.Pruned, "Finalize aggregates the breakdown")
		})
	}
}

// Pooled memo buffers must be logically empty on acquire: interleaved shapes
// (forcing growth, reset and reuse) produce identical results to the brute
// force reference, sequentially and concurrently.
func TestCountShapeMemoReuse(t *testing.T) {
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // deterministic test seed
	g := graph.New(6)
	sc := shapecount.New(g)

	type shapeCase struct {
		ends []int
		want []uint64
		mask state.State
	}
	var cases []shapeCase
	for len(cases) < 12 {
		mask, ok := randomWalkMask(rng, g, 10+rng.Intn(5)) // sizes 10..14: tables grow past initial cap
		if !ok {
			continue
		}
		var ends []int
		for e := range mask.AllVisited() {
			ends = append(ends, e)
		}
		want := make([]uint64, len(ends))
		for i, e := range ends {
			want[i] = bruteForceH(g, mask, e)
		}
		cases = append(cases, shapeCase{mask: mask, ends: ends, want: want})
	}

	check := func(sc *shapecount.Counter, c shapeCase) {
		got := sc.CountShape(c.mask, c.ends, nil)
		assert.Equal(t, c.want, got, "mask=%b", uint64(c.mask))
	}

	for range 5 { // repeated passes exercise pooled reuse and generation reset
		for _, c := range cases {
			check(sc, c)
		}
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			conc := shapecount.New(g)
			for _, c := range cases {
				check(conc, c)
			}
		})
	}
	wg.Wait()
}

// The persistent tail memo must never change h: a shape counted with a table
// already filled by other shapes still returns brute-force values (a stored
// subproblem "arrived from another shape"), re-counting hits the table, and a
// nil tail is bit-identical to CountShape (plan 03 correctness gate).
func TestCountShapeTailMemo(t *testing.T) {
	rng := rand.New(rand.NewSource(11)) //nolint:gosec // deterministic test seed
	g := graph.New(6)
	sc := shapecount.New(g)
	sc.SetTailMemo(49, 0) // persist every state: maximal cross-shape reuse

	maskA, ok := randomWalkMask(rng, g, 12)
	require.True(t, ok)

	// maskB is a strict superset of maskA (two adjacent cells added): its DP
	// descends into subproblems that counting A already stored in the tail.
	maskB := maskA
	for added := 0; added < 2; {
		progressed := false
		for c := range maskB.AllVisited() {
			for n := range g.GetNeighborMask(c).AllVisited() {
				if maskB.IsUnvisited(n) {
					maskB = maskB.Visit(n)
					added++
					progressed = true
					break
				}
			}
			if progressed {
				break
			}
		}
		require.True(t, progressed, "must grow the superset")
	}

	count := func(mask state.State) ([]int, []uint64) {
		var ends []int
		for e := range mask.AllVisited() {
			ends = append(ends, e)
		}
		want := make([]uint64, len(ends))
		for i, e := range ends {
			want[i] = bruteForceH(g, mask, e)
		}
		return ends, want
	}
	endsA, wantA := count(maskA)
	endsB, wantB := count(maskB)

	tail := sc.NewTail()
	require.NotNil(t, tail, "tail level must be enabled")

	var resA1 types.Result
	assert.Equal(t, wantA, sc.CountShapeWithTail(maskA, endsA, &resA1, tail), "maskA=%b", uint64(maskA))

	var resB types.Result
	assert.Equal(t, wantB, sc.CountShapeWithTail(maskB, endsB, &resB, tail), "shared-table maskB=%b", uint64(maskB))

	var resA2 types.Result
	assert.Equal(t, wantA, sc.CountShapeWithTail(maskA, endsA, &resA2, tail), "re-counted maskA=%b", uint64(maskA))
	assert.Positive(t, resA2.TailHits, "re-counted shape must reuse stored subproblems")

	// A fresh table reproduces the values as well; entries only grow.
	tail2 := sc.NewTail()
	var resFresh types.Result
	assert.Equal(t, wantB, sc.CountShapeWithTail(maskB, endsB, &resFresh, tail2))
	assert.Positive(t, tail2.Entries())

	// nil tail is exactly CountShape.
	assert.Equal(t, sc.CountShape(maskA, endsA, nil), sc.CountShapeWithTail(maskA, endsA, nil, nil))
}

// L2 must never change h: the same shapes evaluated with the enhanced DP
// pruning and with it disabled return identical values, while the pruned run
// evaluates strictly fewer DP states (plan 04 correctness gate).
func TestCountShapeL2Equivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(5)) //nolint:gosec // deterministic test seed

	for _, size := range []int{5, 6} {
		g := graph.New(size)
		withL2 := shapecount.New(g)
		withL2.SetL2(pruner.L2All, pruner.DefaultMinL2)
		noL2 := shapecount.New(g)
		noL2.SetL2(pruner.L2None, 0)

		var l2Res, offRes types.Result
		for range 40 {
			mask, ok := randomWalkMask(rng, g, 13+rng.Intn(7)) // sizes 13..19: L2 threshold reachable
			if !ok {
				continue
			}
			var ends []int
			for e := range mask.AllVisited() {
				ends = append(ends, e)
			}
			want := make([]uint64, len(ends))
			for i, e := range ends {
				want[i] = bruteForceH(g, mask, e)
			}
			assert.Equal(t, want, withL2.CountShape(mask, ends, &l2Res), "mask=%b", uint64(mask))
			assert.Equal(t, want, noL2.CountShape(mask, ends, &offRes), "mask=%b", uint64(mask))
		}

		assert.Less(t, l2Res.DPStates, offRes.DPStates, "L2 must shrink the evaluated DP set")
		assert.Positive(t, l2Res.PrunedArticulation+l2Res.PrunedForcedChain,
			"size %d sample must exercise both L2 cuts", size)
		l2Res.Finalize()
		assert.Equal(t,
			l2Res.PrunedDeadEnd+l2Res.PrunedNoCont+l2Res.PrunedDisconn+l2Res.PrunedEndpoints+
				l2Res.PrunedArticulation+l2Res.PrunedForcedChain,
			l2Res.Pruned)
	}
}

// Shape feasibility filter (plan 02): a fully killed shape returns zeros
// without touching the DP and raises FilteredShapes; partial kills keep the
// exact values.
func TestCountShapeFeasibilityFilter(t *testing.T) {
	g := graph.New(6)
	sc := shapecount.New(g) // default mask: forced chains

	chainKilled := state.State(0x311198260) | state.Bit(29)
	var res types.Result
	got := sc.CountShape(chainKilled, []int{29}, &res)
	assert.Equal(t, []uint64{0}, got)
	assert.Equal(t, 1, res.FilteredShapes)
	assert.Zero(t, res.DPStates, "a filtered shape must not run the DP")

	// The K1,3 star needs the articulation check; with the default chain-only
	// mask it is not filtered but still evaluates to zeros.
	star := state.NewState(14, 1, 3, 6)
	res = types.Result{}
	got = sc.CountShape(star, []int{14, 1, 3, 6}, &res)
	assert.Equal(t, []uint64{0, 0, 0, 0}, got)
	assert.Zero(t, res.FilteredShapes)

	sc.SetShapeFilter(pruner.L2Articulation)
	res = types.Result{}
	got = sc.CountShape(star, []int{14, 1, 3, 6}, &res)
	assert.Equal(t, []uint64{0, 0, 0, 0}, got)
	assert.Equal(t, 1, res.FilteredShapes)

	// A live shape is never filtered.
	res = types.Result{}
	got = sc.CountShape(state.NewState(0, 8), []int{0}, &res)
	assert.Equal(t, []uint64{1}, got)
	assert.Zero(t, res.FilteredShapes)
}

// genPrefix samples a legal generation prefix: a random knight walk whose
// every intermediate remainder passes ShouldPruneAfterVisit — exactly the
// invariant the searcher's DFS maintains, so the complements have the same
// shape distribution as the final pass input.
func genPrefix(rng *rand.Rand, g *graph.Graph, p *pruner.Pruner, depth int) (state.State, bool) {
	full := state.State(0).Invert(g.GetTotalCells())
	for range 100 {
		cur := rng.Intn(g.GetTotalCells())
		prefix := state.Bit(cur)
		for range depth - 1 {
			unvisited := full.AndNot(prefix)
			var cand []int
			for n := range g.GetNeighborMask(cur).Intersect(unvisited).AllVisited() {
				if pruned, _ := p.ShouldPruneAfterVisit(n, unvisited.Unvisit(n)); !pruned {
					cand = append(cand, n)
				}
			}
			if len(cand) == 0 {
				break
			}
			cur = cand[rng.Intn(len(cand))]
			prefix = prefix.Visit(cur)
		}
		if prefix.CountBits() == depth {
			return prefix, true
		}
	}
	return 0, false
}

// The filter must not change any h value: filtered and unfiltered counters
// agree on complements of pruned generation prefixes (the distribution the
// final pass sees) with small end sets, exactly like the M accumulator.
func TestCountShapeFilterEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(5)) //nolint:gosec // deterministic test seed

	for _, size := range []int{5, 6} {
		g := graph.New(size)
		p := pruner.New(g)
		on := shapecount.New(g)
		off := shapecount.New(g)
		off.SetShapeFilter(pruner.L2None)

		full := state.State(0).Invert(g.GetTotalCells())
		filtered, shapes := 0, 0
		for trial := range 300 {
			prefix, ok := genPrefix(rng, g, p, size*size/2)
			if !ok {
				continue
			}
			mask := full.AndNot(prefix)

			var ends []int
			for e := range mask.AllVisited() {
				ends = append(ends, e)
			}
			rng.Shuffle(len(ends), func(i, j int) { ends[i], ends[j] = ends[j], ends[i] })
			ends = ends[:min(len(ends), 4)] // small queried end set, as in M shards

			var res types.Result
			gotOn := on.CountShape(mask, ends, &res)
			gotOff := off.CountShape(mask, ends, nil)
			require.Equalf(t, gotOff, gotOn, "size=%d trial=%d mask=%b ends=%v", size, trial, uint64(mask), ends)
			filtered += res.FilteredShapes
			shapes++
		}
		assert.Positive(t, filtered, "filter must fire on the size %d sample (%d shapes)", size, shapes)
	}
}
