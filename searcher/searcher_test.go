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
	"knighttour/pruner"
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

// GenerateTasks at depth = totalCells: every leaf is a full tour, so the summed
// orbit weights must reproduce the known total (D4-invariance of tour counts
// makes per-group weighting exact; SholdSkip starts contribute zero tours).
func TestFullCountViaGenerateTasksMatchesKnownTotal(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	c := cache.NewCache()
	for _, group := range sym.GetCanonicalGroups() {
		searcher.GenerateTasks(context.Background(), c, group.Canonical, uint64(group.OrbitSize), g.GetTotalCells())
	}

	var total int64
	for _, e := range allTasks(t, c) {
		assert.Equal(t, g.GetTotalCells(), e.Path.State().CountBits(), "leaf must cover the whole board")
		total += int64(e.Weight)
	}

	assert.Equal(t, int64(1728), total, "Σ orbit weights over full-depth leaves == plain count")
}

func TestGenerateTasksEmitsCanonicalPrefixes(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	c := cache.NewCache()
	result := searcher.GenerateTasks(context.Background(), c, 0, 4, 3)

	assert.Positive(t, result.CacheWrites, "depth=3 must emit prefixes")
	for _, e := range allTasks(t, c) {
		assert.Equal(t, 3, e.Path.State().CountBits(), "every prefix sits at target depth")
		assert.Positive(t, e.Weight)
	}
}

func TestGenerateTasksDepthZeroEmitsStart(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	c := cache.NewCache()
	result := searcher.GenerateTasks(context.Background(), c, 0, 1, 0)

	assert.Equal(t, 1, result.CacheWrites, "depth=0 emits the start itself")
	assert.Equal(t, 1, c.ItemsCount(), "depth=0 stores exactly one record")
}

// --- task-cache generation and reversal count (specs/searcher.md, ADR-011) --

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
// task-by-task with naiveCountFrom: f(task) must equal the plain completions
// of (state, end), not merely sum up right.
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
					ref := naiveCountFrom(g, e.Path.State(), e.Path.End())
					assert.Equal(t, uint64(ref), uint64(res.TotalPathsFound), "task %d: f must match the brute force", i)
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

// --- write gate lemma (specs/searcher.md) ---

// gateLeafWalk mirrors the dfsTask descent — L0/L1 pruning included — down to
// a fixed depth and applies the write gate at every leaf, collecting the keys
// it rejects.
func gateLeafWalk(g *graph.Graph, pr *pruner.Pruner, st state.State, end, depth int, rejected map[path.Path]bool) {
	if st.CountBits() == depth {
		todo := st.Invert(g.GetTotalCells())
		if cut, _ := pr.ShouldPruneState(end, todo); cut {
			rejected[path.New(st, end)] = true
		}
		return
	}

	unvisited := st.Invert(g.GetTotalCells())
	for n := range g.GetNeighborMask(end).Intersect(unvisited).AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() {
			if cut, _ := pr.ShouldPruneAfterVisit(n, newUnvisited); cut {
				continue
			}
		}
		gateLeafWalk(g, pr, st.Visit(n), n, depth, rejected)
	}
}

// The soundness lemma of the write gate: every generation leaf the gate
// rejects has f(key) = 0 — no Hamiltonian path from the end covers the
// remainder. Exhaustive descent on 5×5 to a fixed depth (the production
// L0/L1 pruning included), each rejected key re-checked against the
// independent brute-force oracle.
func TestWriteGateRejectsOnlyDeadKeys(t *testing.T) {
	const size = 5
	const depth = 12 // half the board: the gate fires, the oracle stays cheap

	g := graph.New(size)
	sym := symmetry.NewSymmetry(size)
	searcher := NewSearcher(g, sym)

	rejected := make(map[path.Path]bool)
	for start := range g.GetTotalCells() {
		if g.SholdSkip(start) {
			continue
		}
		gateLeafWalk(g, searcher.pruner, state.NewState(start), start, depth, rejected)
	}

	require.NotEmpty(t, rejected, "the gate must reject some depth-%d keys on 5x5", depth)
	for key := range rejected {
		assert.Zero(t, naiveCountFrom(g, key.State(), key.End()),
			"rejected key %v must have f = 0", key)
	}
}
