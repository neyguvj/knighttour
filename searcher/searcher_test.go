package searcher

import (
	"context"
	"math/rand"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/cache"
	"knighttour/graph"
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
// makes per-group weighting exact; ShouldSkip starts contribute zero tours).
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
		assert.Equal(t, g.GetTotalCells(), e.Mask.CountBits(), "leaf must cover the whole board")
		total += int64(e.Weight)
	}

	assert.Equal(t, int64(1728), total, "Σ orbit weights over full-depth leaves == plain count")
}

// Leaves are class slots at the target depth: every emitted mask sits exactly
// at depth and its weight is a multiple of the group's orbit size (a slot
// sums per-prefix contributions of that orbit).
func TestGenerateTasksEmitsCanonicalPrefixes(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	c := cache.NewCache()
	result := searcher.GenerateTasks(context.Background(), c, 0, 4, 3)

	assert.Positive(t, result.CacheWrites, "depth=3 must emit prefixes")
	for _, e := range allTasks(t, c) {
		assert.Equal(t, 3, e.Mask.CountBits(), "every prefix sits at target depth")
		assert.Positive(t, e.Weight)
		assert.Zero(t, e.Weight%4, "slot weight is a multiple of the group orbit")
	}
}

func TestGenerateTasksDepthZeroEmitsStart(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	c := cache.NewCache()
	result := searcher.GenerateTasks(context.Background(), c, 0, 1, 0)

	assert.Equal(t, 1, result.CacheWrites, "depth=0 emits the start itself")
	assert.Equal(t, 1, c.Seal().Len(), "depth=0 stores exactly one record")
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

// allTasks flattens a task-cache into one slice via the sealed View's All
// walk — expanded per class, the lock-free direct iteration the count phase
// uses (plan 16, plan 18). A single walk keeps the append race-free without an
// extra lock.
func allTasks(tb testing.TB, c *cache.Cache) []cache.Entry {
	tb.Helper()
	view := c.Seal()
	out := make([]cache.Entry, 0, view.Len())
	for e := range view.All(context.Background()) {
		out = append(out, e)
	}
	return out
}

// The reversal identity Σ_tasks W·f == plain count, pinned per split depth
// against the naive brute-force total (1728). A sample of class tasks is
// checked task-by-task with naiveCountFrom: f(mask, rep as end) must equal the
// plain completions of that placement, not merely sum up right.
func TestReversalMatchesBruteForceAllDepths(t *testing.T) {
	const size = 5
	const naiveSample = 8

	g := graph.New(size)
	sym := symmetry.NewSymmetry(size)
	searcher := NewSearcher(g, sym)

	for d := 1; d <= g.GetTotalCells()/2; d++ {
		t.Run("depth"+strconv.Itoa(d), func(t *testing.T) {
			taskCache := buildTaskCache(g, searcher, d)
			view := taskCache.Seal()
			require.Positive(t, view.Len())

			var sum uint64
			for i, e := range allTasks(t, taskCache) {
				res := searcher.CountPathsWithCacheReversal(context.Background(), e, view, d)
				sum += e.Weight * uint64(res.TotalPathsFound)

				if i < naiveSample {
					ref := naiveCountFrom(g, e.Mask, int(e.Rep))
					assert.Equal(t, uint64(ref), uint64(res.TotalPathsFound), "task %d: f must match the brute force", i)
				}
			}
			assert.Equal(t, uint64(1728), sum, "Σ W·f == plain count at depth %d", d)
		})
	}
}

// Degenerate phase B: an entry already at the target depth is written as-is
// into its own (mask, rep) slot — no descent, no re-canonicalization.
func TestExtendTaskWritesEntryAsIsAtTargetDepth(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	e := cache.Entry{Mask: state.State(0).Visit(0).Visit(6), Rep: 6, Weight: 3}

	c := cache.NewCache()
	result := searcher.ExtendTask(context.Background(), c, e, 2)

	assert.Equal(t, 1, result.CacheWrites, "entry at target depth writes itself")
	view := c.Seal()
	val, found := view.Get(e.Mask)
	require.True(t, found, "record must be stored as-is under the entry mask")
	w, ok := val.Weight(e.Rep)
	assert.True(t, ok, "class slot must be the entry's rep")
	assert.Equal(t, uint64(3), w)
	assert.Equal(t, 1, view.Len())
}

// c == nil and 2d > totalCells both disable reversal: the count must equal
// the naive full descent and never touch a cache (zero hits/misses).
func TestCountPathsWithCacheReversalFullDescentIdentities(t *testing.T) {
	const size = 5
	g := graph.New(size)
	sym := symmetry.NewSymmetry(size)
	searcher := NewSearcher(g, sym)

	tests := []struct {
		c    *cache.View
		name string
		d    int
	}{
		{name: "nil cache", c: nil, d: 6},
		{name: "beyond duality", c: cache.NewCache().Seal(), d: size*size/2 + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for start := range g.GetTotalCells() {
				st := state.NewState(start)
				task := cache.Entry{Mask: st, Rep: uint8(start)}
				res := searcher.CountPathsWithCacheReversal(context.Background(), task, tt.c, tt.d)
				assert.Equal(t, naiveCountFrom(g, st, start), res.TotalPathsFound, "start %d", start)
				assert.Zero(t, res.CacheHits+res.CacheMisses, "full descent must not consult the cache")
			}
		})
	}
}

// The stop fires exactly at level totalCells−d: a task whose bits already sit
// at the stop level answers through completions immediately — every u ∈
// N(end)∩U is one class probe and nothing else. An empty cache gives 0 paths
// with exactly |N(end)∩U| misses; seeding one class slot with its orbit
// weight contributes exactly W/orbitSize == 1 per candidate of that class,
// while candidates of other classes keep missing (per-class accounting).
func TestReversalStopLookupsHappenAtStopLevel(t *testing.T) {
	const size = 5
	total := size * size
	g := graph.New(size)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	st := state.NewState(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12) // bits == stopLevel
	task := cache.Entry{Mask: st, Rep: 12}
	d := total / 2 // stopLevel = total−d = 13 == bits: immediate completions
	unvisited := st.Invert(total)

	cand := g.GetNeighborMask(12).Intersect(unvisited)
	require.Positive(t, cand.CountBits())

	res := searcher.CountPathsWithCacheReversal(context.Background(), task, cache.NewCache().Seal(), d)
	assert.Zero(t, res.TotalPathsFound, "empty cache answers zero completions")
	assert.Zero(t, res.CacheHits)
	assert.Equal(t, cand.CountBits(), res.CacheMisses, "one class probe per candidate end at the stop level")

	var u0 int
	for u := range cand.AllVisited() {
		u0 = u
		break
	}
	K, rep, orbitSize := sym.CanonicalClass(unvisited, u0)
	c := cache.NewCache()
	c.Set(K, rep, uint64(orbitSize))

	want := 0
	for u := range cand.AllVisited() {
		if k, r, _ := sym.CanonicalClass(unvisited, u); k == K && r == rep {
			want++
		}
	}

	res = searcher.CountPathsWithCacheReversal(context.Background(), task, c.Seal(), d)
	assert.Equal(t, want, res.TotalPathsFound, "each candidate in the seeded class adds W/orbitSize == 1")
	assert.Equal(t, want, res.CacheHits)
	assert.Equal(t, cand.CountBits()-want, res.CacheMisses)
}

// ShouldSkip starts contribute nothing to the task cache (odd-board parity).
func TestGenerateTasksSkipsWrongColor(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	var skipped = -1
	for p := range g.GetTotalCells() {
		if g.ShouldSkip(p) {
			skipped = p
			break
		}
	}
	require.NotEqual(t, -1, skipped)

	c := cache.NewCache()
	result := searcher.GenerateTasks(context.Background(), c, skipped, 1, 3)

	assert.Zero(t, result.CacheWrites, "ShouldSkip start emits nothing")
	assert.Zero(t, c.Seal().Len())
}

// Phase B through a batched Staging sink must land exactly the task-cache that
// direct writes produce (ADR-031): same class slots, same summed weights.
func TestExtendTaskViaStagingMatchesDirect(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	ctx := context.Background()

	intermediate := cache.NewCache()
	for _, start := range []int{0, 12} {
		searcher.GenerateTasks(ctx, intermediate, start, 8, 3)
	}
	var worklist []cache.Entry
	for e := range intermediate.Seal().All(ctx) {
		worklist = append(worklist, e)
	}
	require.NotEmpty(t, worklist)

	dump := func(c *cache.Cache) map[state.State]map[uint8]uint64 {
		m := make(map[state.State]map[uint8]uint64)
		for e := range c.Seal().All(ctx) {
			if m[e.Mask] == nil {
				m[e.Mask] = make(map[uint8]uint64)
			}
			m[e.Mask][e.Rep] = e.Weight
		}
		return m
	}

	direct := cache.NewCache()
	staged := cache.NewCache()
	sink := staged.NewStaging(7) // force several auto-flushes across the worklist
	for _, e := range worklist {
		searcher.ExtendTask(ctx, direct, e, 6)
		searcher.ExtendTask(ctx, sink, e, 6)
	}
	sink.Flush()

	assert.Equal(t, dump(direct), dump(staged), "batched writes land the same table")
}

// --- writer/reader canonicalization agreement (specs/searcher.md) ---

// randomPlacement is a deterministic (mask, end) test pair with end ∈ mask.
type randomPlacement struct {
	st  state.State
	end int
}

// randomPlacements draws count random placements from a fixed seed.
func randomPlacements(size int, seed int64, count int) []randomPlacement {
	rnd := rand.New(rand.NewSource(seed))
	total := size * size
	out := make([]randomPlacement, 0, count)
	for range count {
		var st state.State
		for range 1 + rnd.Intn(total) {
			st = st.Visit(rnd.Intn(total))
		}
		end := 0
		for u := range st.AllVisited() {
			end = u
		}
		out = append(out, randomPlacement{st, end})
	}
	return out
}

// The writer's CanonicalClass and the reader's composition
// CanonicalMaskFrame → TransformCell → ClassRep are one and the same class
// form on random placements (fixed seed), and the result does not depend on
// which argmin frame the reader happened to get.
func TestWriterReaderCanonicalizationAgree(t *testing.T) {
	sym := symmetry.NewSymmetry(5)
	for _, p := range randomPlacements(5, 1811, 200) {
		K, rep, orbitSize := sym.CanonicalClass(p.st, p.end)

		frameK, frame, stab := sym.CanonicalMaskFrame(p.st)
		c := sym.TransformCell(frame, p.end)
		assert.Equal(t, K, frameK, "shared mask")
		assert.Equal(t, rep, sym.ClassRep(stab, c), "reader form equals writer form")
		assert.Equal(t, orbitSize, sym.CellOrbitSize(stab, int(rep)), "shared orbit size")
	}

	t.Run("argmin-frame independence on the full board", func(t *testing.T) {
		// Every transform is an argmin frame of the full mask, so this probes
		// the class form against all eight frames at once.
		full := state.State(1<<25 - 1)
		K, _, stab := sym.CanonicalMaskFrame(full)
		for end := range 25 {
			wantK, rep, orbitSize := sym.CanonicalClass(full, end)
			assert.Equal(t, wantK, K, "mask")
			assert.Positive(t, orbitSize)
			for f := range symmetry.NumTransforms {
				c := sym.TransformCell(uint8(f), end)
				assert.Equalf(t, rep, sym.ClassRep(stab, c), "end %d frame %d", end, f)
				assert.Equalf(t, orbitSize, sym.CellOrbitSize(stab, int(rep)), "end %d frame %d", end, f)
			}
		}
	})
}

// --- write gate lemma (specs/searcher.md) ---

// gateLeafWalk mirrors the dfsTask descent — L0/L1 pruning included — down to
// a fixed depth and applies the write gate at every leaf, collecting the end
// classes it rejects in canonical form.
func gateLeafWalk(g *graph.Graph, sym *symmetry.Symmetry, pr *pruner.Pruner, st state.State, end, depth int, rejected map[cache.Entry]bool) {
	if st.CountBits() == depth {
		todo := st.Invert(g.GetTotalCells())
		if cut, _ := pr.ShouldPruneState(end, todo); cut {
			K, rep, _ := sym.CanonicalClass(st, end)
			rejected[cache.Entry{Mask: K, Rep: rep}] = true
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
		gateLeafWalk(g, sym, pr, st.Visit(n), n, depth, rejected)
	}
}

// The soundness lemma of the write gate: every generation leaf-class the gate
// rejects has f(class) = 0 — no Hamiltonian path from the class end covers the
// remainder. Exhaustive descent on 5×5 to a fixed depth (the production L0/L1
// pruning included), each rejected class re-checked against the independent
// brute-force oracle at its representative.
func TestWriteGateRejectsOnlyDeadClasses(t *testing.T) {
	const size = 5
	const depth = 12 // half the board: the gate fires, the oracle stays cheap

	g := graph.New(size)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	rejected := make(map[cache.Entry]bool)
	for start := range g.GetTotalCells() {
		if g.ShouldSkip(start) {
			continue
		}
		gateLeafWalk(g, sym, searcher.pruner, state.NewState(start), start, depth, rejected)
	}

	require.NotEmpty(t, rejected, "the gate must reject some depth-%d classes on 5x5", depth)
	for class := range rejected {
		assert.Zero(t, naiveCountFrom(g, class.Mask, int(class.Rep)),
			"rejected class %v/%d must have f = 0", class.Mask, class.Rep)
	}
}
