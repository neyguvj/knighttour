package searcher

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/path"
	"knighttour/shapecount"
	"knighttour/state"
	"knighttour/symmetry"
)

// naiveCountFrom is the independent brute-force oracle: plain backtracking over
// adjacency lists with a visited slice — no bitboards, no pruning, no symmetry.
// It counts paths covering every remaining cell from (st, end).
func naiveCountFrom(g *graph.Graph, st state.State, end int) int {
	total := g.GetTotalCells()
	visited := make([]bool, total)
	for p := range total {
		visited[p] = st.IsVisited(p)
	}

	var walk func(cur, covered int) int
	walk = func(cur, covered int) int {
		if covered == total {
			return 1
		}
		found := 0
		for _, n := range g.GetNeighbors(cur) {
			if !visited[n] {
				visited[n] = true
				found += walk(n, covered+1)
				visited[n] = false
			}
		}
		return found
	}

	return walk(end, st.CountBits())
}

// The known total: open tours over all starts on 5x5, pinned by the oracle alone.
func TestNaiveBruteForceMatchesKnownTotal(t *testing.T) {
	g := graph.New(5)

	total := 0
	for start := range g.GetTotalCells() {
		total += naiveCountFrom(g, state.NewState(start), start)
	}

	assert.Equal(t, 1728, total, "Expected 1728 for 5x5 board")
}

// GenerateRoots at depth = totalCells: every leaf is a full tour, so the summed
// orbit weights must reproduce the known total (D4-invariance of tour counts
// makes per-group weighting exact; SholdSkip starts contribute zero tours).
func TestFullCountViaGenerateRootsMatchesKnownTotal(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	acc := cache.NewAccumulator()
	for _, group := range sym.GetCanonicalGroups() {
		sink := acc.Local()
		searcher.GenerateRoots(context.Background(), sink, group.Canonical, uint64(group.OrbitSize), g.GetTotalCells())
		sink.Flush()
	}

	var total int64
	for _, e := range acc.Drain() {
		assert.Equal(t, g.GetTotalCells(), e.Path.State().CountBits(), "leaf must cover the whole board")
		total += int64(e.Weight)
	}

	assert.Equal(t, int64(1728), total, "Σ orbit weights over full-depth leaves == plain count")
}

// shallowOracle counts full extensions of p through phase B at depth total-1:
// each complete path emits exactly one singleton complement class and
// h({u}, u) == 1, so Σ weight·h(C) is the exact plain count.
func shallowOracle(t *testing.T, g *graph.Graph, searcher *Searcher, p path.Path, weight uint64) int64 {
	t.Helper()

	acc := cache.NewAccumulator()
	sink := acc.Local()
	searcher.ExtendToClasses(context.Background(), sink, p, weight, g.GetTotalCells()-1)
	sink.Flush()

	sc := shapecount.New(g)
	var total int64
	for _, e := range acc.Drain() {
		assert.Equal(t, 1, e.Path.State().CountBits(), "shallow complement must be a single cell")
		h := sc.CountShape(e.Path.State(), []int{e.Path.End()}, nil)[0]
		assert.Equal(t, uint64(1), h, "singleton shape has exactly one covering path")
		total += int64(h) * int64(e.Weight)
	}
	return total
}

func TestGenerateRootsEmitsCanonicalPrefixes(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	acc := cache.NewAccumulator()
	sink := acc.Local()
	result := searcher.GenerateRoots(context.Background(), sink, 0, 4, 3)
	sink.Flush()

	assert.Positive(t, result.CacheWrites, "depth=3 must emit prefixes")
	for _, e := range acc.Drain() {
		assert.Equal(t, 3, e.Path.State().CountBits(), "every prefix sits at target depth")
		assert.Positive(t, e.Weight)
	}
}

func TestGenerateRootsDepthZeroEmitsStart(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	acc := cache.NewAccumulator()
	sink := acc.Local()
	result := searcher.GenerateRoots(context.Background(), sink, 0, 1, 0)
	sink.Flush()

	assert.Equal(t, 1, result.CacheWrites, "depth=0 emits the start itself")
}

func TestGenerateRootsSkipsWrongColor(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	// Find a position the parity filter rejects on the odd board.
	var skipped = -1
	for p := range g.GetTotalCells() {
		if g.SholdSkip(p) {
			skipped = p
			break
		}
	}
	require.NotEqual(t, -1, skipped, "5x5 must filter some starts")

	acc := cache.NewAccumulator()
	sink := acc.Local()
	result := searcher.GenerateRoots(context.Background(), sink, skipped, 1, 3)
	sink.Flush()

	assert.Zero(t, result.CacheWrites, "SholdSkip start emits nothing")
	assert.Zero(t, acc.ItemsCount())
}

// The class-mode identity: Σ h(C)·M(C) over the emitted accumulator must
// reproduce the plain full count from the same roots. The reference is the
// shallow phase-B oracle (depth = totalCells-1, singleton complements with
// h == 1), cross-checked against the naive brute force on a small sample.
func TestExtendToClassesMatchesFullCount(t *testing.T) {
	const baseDepth = 5
	const naiveSampleSize = 8

	tests := []struct {
		size  int
		depth int // target depth; complement size total-depth must stay DP-cheap
	}{
		{size: 5, depth: 13}, // mid roots, medium complements (q=12)
		{size: 5, depth: 20}, // deep roots, small complements (q=5)
	}

	for _, tt := range tests {
		t.Run("size"+strconv.Itoa(tt.size)+"/depth"+strconv.Itoa(tt.depth), func(t *testing.T) {
			g := graph.New(tt.size)
			sym := symmetry.NewSymmetry(tt.size)
			searcher := NewSearcher(g, sym)

			intermediate := cache.NewAccumulator()
			for _, group := range sym.GetCanonicalGroups() {
				sink := intermediate.Local()
				searcher.GenerateRoots(context.Background(), sink, group.Canonical, uint64(group.OrbitSize), baseDepth)
				sink.Flush()
			}

			entries := intermediate.Drain()

			acc := cache.NewAccumulator()
			sink := acc.Local()
			var fullTotal int64
			for i, e := range entries {
				searcher.ExtendToClasses(context.Background(), sink, e.Path, e.Weight, tt.depth)
				fullTotal += shallowOracle(t, g, searcher, e.Path, e.Weight)

				if i < naiveSampleSize {
					ref := int64(naiveCountFrom(g, e.Path.State(), e.Path.End())) * int64(e.Weight)
					assert.Equal(t, ref, shallowOracle(t, g, searcher, e.Path, e.Weight),
						"shallow oracle must match the naive brute force (entry %d)", i)
				}
			}
			sink.Flush()

			sc := shapecount.New(g)
			var classTotal int64
			for _, ce := range acc.Drain() {
				h := sc.CountShape(ce.Path.State(), []int{ce.Path.End()}, nil)[0]
				classTotal += int64(h) * int64(ce.Weight)
			}

			assert.Positive(t, fullTotal)
			assert.Equal(t, fullTotal, classTotal, "Σ h(C)·M(C) must equal the plain count")
		})
	}
}

// Degenerate split (entry already at target depth): ExtendToClasses emits the
// complement classes of the entry itself instead of descending.
func TestExtendToClassesEmitsAtEntryDepth(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	st := state.State(0).Visit(0).Visit(6)
	p := path.New(st, 6)

	acc := cache.NewAccumulator()
	sink := acc.Local()
	result := searcher.ExtendToClasses(context.Background(), sink, p, 3, 2)
	sink.Flush()

	assert.Positive(t, result.CacheWrites, "entry at target depth emits immediately")
	var total uint64
	for _, e := range acc.Drain() {
		total += e.Weight
	}
	cand := g.GetNeighborMask(6).Intersect(st.Invert(g.GetTotalCells()))
	assert.Equal(t, uint64(cand.CountBits())*3, total, "each neighbor end gets the entry weight")
}
