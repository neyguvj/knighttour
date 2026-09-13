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

// --- reversal mode (specs/searcher.md, ADR-011) -----------------------------

// buildTaskCache fills a task cache at depth d from every canonical group,
// mirroring what the counter's generation phases do.
func buildTaskCache(g *graph.Graph, searcher *Searcher, d int) *cache.Cache {
	c := cache.NewCache()
	sym := symmetry.NewSymmetry(g.Size())
	for _, group := range sym.GetCanonicalGroups() {
		searcher.GenerateTasks(context.Background(), c, group.Canonical, uint64(group.OrbitSize), d)
	}
	return c
}

// allTasks flattens a task-cache into one slice via Each — the direct shard
// walk the count phase uses after plan 09 removed the Cache snapshots. One
// worker keeps the append race-free without an extra lock.
func allTasks(tb testing.TB, c *cache.Cache) []cache.Entry {
	tb.Helper()
	out := make([]cache.Entry, 0, c.ItemsCount())
	err := c.Each(context.Background(), 1, func(_ context.Context, p path.Path, w uint64) error {
		out = append(out, cache.Entry{Path: p, Weight: w})
		return nil
	})
	require.NoError(tb, err)
	return out
}

// The reversal identity Σ_tasks W·f == plain count, pinned per split depth
// against the naive brute-force total (1728). A sample of tasks is checked
// task-by-task with shallowOracle (phase B at totalCells−1, itself pinned to
// naiveCountFrom in TestExtendToClassesMatchesFullCount): f(task) must equal
// the plain completions of (state, end), not merely sum up right.
func TestReversalMatchesBruteForceAllDepths(t *testing.T) {
	const size = 5
	const naiveSample = 8

	g := graph.New(size)
	sym := symmetry.NewSymmetry(size)
	searcher := NewSearcher(g, sym)

	for d := 1; d <= g.GetTotalCells()/2; d++ {
		t.Run("depth"+strconv.Itoa(d), func(t *testing.T) {
			taskCache := buildTaskCache(g, searcher, d)
			require.Positive(t, taskCache.ItemsCount())

			var sum uint64
			for i, e := range allTasks(t, taskCache) {
				res := searcher.CountPathsWithCacheReversal(context.Background(), e.Path, taskCache, d)
				sum += e.Weight * uint64(res.TotalPathsFound)

				if i < naiveSample {
					ref := shallowOracle(t, g, searcher, e.Path, 1)
					assert.Equal(t, uint64(ref), uint64(res.TotalPathsFound), "task %d: f must match the oracle", i)
				}
			}
			assert.Equal(t, uint64(1728), sum, "Σ W·f == plain count at depth %d", d)
		})
	}
}

// Degenerate phase B: an entry already at the target depth is written as-is
// (no descent, no re-canonicalization).
func TestExtendTaskWritesEntryAsIsAtTargetDepth(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	p := path.New(state.State(0).Visit(0).Visit(6), 6)

	c := cache.NewCache()
	result := searcher.ExtendTask(context.Background(), c, p, 3, 2)

	assert.Equal(t, 1, result.CacheWrites, "entry at target depth writes itself")
	weight, found := c.Get(p)
	assert.True(t, found, "record must be stored as-is under the entry key")
	assert.Equal(t, uint64(3), weight)
	assert.Equal(t, 1, c.ItemsCount())
}

// c == nil and 2d > totalCells both disable reversal: the count must equal
// the naive full descent and never touch a cache (zero hits/misses).
func TestCountPathsWithCacheReversalFullDescentIdentities(t *testing.T) {
	const size = 5
	g := graph.New(size)
	sym := symmetry.NewSymmetry(size)
	searcher := NewSearcher(g, sym)

	tests := []struct {
		c    *cache.Cache
		name string
		d    int
	}{
		{name: "nil cache", c: nil, d: 6},
		{name: "beyond duality", c: cache.NewCache(), d: size*size/2 + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for start := range g.GetTotalCells() {
				st := state.NewState(start)
				res := searcher.CountPathsWithCacheReversal(context.Background(), path.New(st, start), tt.c, tt.d)
				assert.Equal(t, naiveCountFrom(g, st, start), res.TotalPathsFound, "start %d", start)
				assert.Zero(t, res.CacheHits+res.CacheMisses, "full descent must not consult the cache")
			}
		})
	}
}

// The stop fires exactly at level totalCells−d: a task whose bits already sit
// at the stop level answers through Completions immediately — every u ∈
// N(end)∩U is looked up once and nothing else. An empty cache gives 0 paths
// with exactly |N(end)∩U| misses; seeding one canonical key with its orbit
// weight contributes exactly W/orbitSize == 1 per candidate sharing that key.
func TestReversalStopLookupsHappenAtStopLevel(t *testing.T) {
	const size = 5
	total := size * size
	g := graph.New(size)
	sym := symmetry.NewSymmetry(size)
	searcher := NewSearcher(g, sym)

	st := state.NewState(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12) // bits == stopLevel
	p := path.New(st, 12)
	d := total / 2 // stopLevel = total−d = 13 == bits: immediate completions
	unvisited := st.Invert(total)

	cand := g.GetNeighborMask(12).Intersect(unvisited)
	require.Positive(t, cand.CountBits())

	res := searcher.CountPathsWithCacheReversal(context.Background(), p, cache.NewCache(), d)
	assert.Zero(t, res.TotalPathsFound, "empty cache answers zero completions")
	assert.Zero(t, res.CacheHits)
	assert.Equal(t, cand.CountBits(), res.CacheMisses, "one lookup per candidate end at the stop level")

	var u0 int
	for u := range cand.AllVisited() {
		u0 = u
		break
	}
	canonical, orbitSize := sym.CanonicalizeWithOrbitSize(unvisited, u0)
	c := cache.NewCache()
	c.Set(canonical, uint64(orbitSize))

	want := 0
	for u := range cand.AllVisited() {
		if sym.Canonicalize(unvisited, u) == canonical {
			want++
		}
	}

	res = searcher.CountPathsWithCacheReversal(context.Background(), p, c, d)
	assert.Equal(t, want, res.TotalPathsFound, "each candidate under the seeded key adds W/orbitSize == 1")
	assert.Equal(t, want, res.CacheHits)
	assert.Equal(t, cand.CountBits()-want, res.CacheMisses)
}

// SholdSkip starts contribute nothing to the task cache (odd-board parity).
func TestGenerateTasksSkipsWrongColor(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	var skipped = -1
	for p := range g.GetTotalCells() {
		if g.SholdSkip(p) {
			skipped = p
			break
		}
	}
	require.NotEqual(t, -1, skipped)

	c := cache.NewCache()
	result := searcher.GenerateTasks(context.Background(), c, skipped, 1, 3)

	assert.Zero(t, result.CacheWrites, "SholdSkip start emits nothing")
	assert.Zero(t, c.ItemsCount())
}
