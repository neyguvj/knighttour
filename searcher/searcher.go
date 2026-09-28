package searcher

import (
	"context"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/pruner"
	"knighttour/state"
	"knighttour/symmetry"
	"knighttour/types"
)

// Searcher is the pure traversal layer (specs/searcher.md): DFS backtracking
// over bitmasks with pruning plus the generation phases and the reversal
// count-DFS. It owns no memo tables — the caches belong to the calling loop.
// The task carrier between phases is cache.Entry (canonical mask + end-class
// tag, plan 18); the package builds no keys of its own types.
type Searcher struct {
	graph  *graph.Graph
	sym    *symmetry.Symmetry
	pruner *pruner.Pruner
}

// NewSearcher returns a searcher for g with D4 canonicalization by sym and the
// stateless L0/L1 pruner attached.
func NewSearcher(g *graph.Graph, sym *symmetry.Symmetry) *Searcher {
	return &Searcher{
		graph:  g,
		sym:    sym,
		pruner: pruner.New(g),
	}
}

// GenerateTasks is the single descent from a start (specs/searcher.md): DFS
// from a canonical start down to depth, writing each leaf placement into c as
// its D4-canonical class form — CanonicalClass(state,end) → Set(K, rep,
// weight) with the group's orbit weight; both the gen-A intermediate table and
// the task-cache go this way (ADR-018). A leaf is written only when it passes
// the write gate; gate cuts are counted in the Result breakdown. ShouldSkip
// starts emit nothing. Statistics: CacheWrites counts Set emissions (class
// contributions).
func (s *Searcher) GenerateTasks(ctx context.Context, c cache.Sink, start int, orbitSize uint64, depth int) (result types.Result) {
	if s.graph.ShouldSkip(start) {
		return result
	}
	s.dfsTask(ctx, state.NewState(start), start, depth, orbitSize, c, &result)
	result.Finalize()
	return result
}

// ExtendTask is phase B of task-cache generation: it continues the task
// descent from an already canonical entry e (mask + end-class tag) with its
// aggregated weight down to the target depth. An entry at or beyond the depth
// (degenerate split, precomputeDepth ≤ base) writes itself as-is into the
// same slot (e.Mask, e.Rep) without the gate — it is exactly the phase-A leaf
// of the same class, already gated by the same check; descending further
// would lose its record. Below the threshold leaves are written canonicalized
// like in GenerateTasks, passing the gate there. The descent from e.Rep is
// legal: rep is a real cell of the mask and f is constant on the class. No
// ShouldSkip check: roots were filtered by phase A.
func (s *Searcher) ExtendTask(ctx context.Context, c cache.Sink, e cache.Entry, depth int) (result types.Result) {
	if e.Mask.CountBits() >= depth {
		c.Set(e.Mask, e.Rep, e.Weight)
		result.CacheWrites++
		result.Finalize()
		return result
	}
	s.dfsTask(ctx, e.Mask, int(e.Rep), depth, e.Weight, c, &result)
	result.Finalize()
	return result
}

// CountPathsWithCacheReversal counts the full completions of task (mask, end =
// task.Rep) with early stop at level totalCells−d (specs/searcher.md): at that
// level the remainder U = full \ T is answered by Σ W(K)[rep(u)]/orbitSize
// over u ∈ N(t) ∩ U from c instead of descending to a full board. task.Weight
// is not used — the calling contour does the weighting. The memo is read
// through the lock-free cache.View (ADR-031, plan 16): the count phase runs
// after the generation barrier and Seal, no writers exist. c == nil or
// 2d > totalCells → full descent without any cache access (the reversal
// duality is unreachable). Statistics: TotalPathsFound, CacheHits/CacheMisses
// per class probe, pruning by reason.
func (s *Searcher) CountPathsWithCacheReversal(ctx context.Context, task cache.Entry, c *cache.View, d int) (result types.Result) {
	stopLevel := -1 // disabled sentinel: full descent
	if c != nil && 2*d <= s.graph.GetTotalCells() {
		stopLevel = s.graph.GetTotalCells() - d
	}
	result.TotalPathsFound = s.dfsCount(ctx, task.Mask, int(task.Rep), stopLevel, c, &result)
	result.Finalize()
	return result
}

// dfsTask is the single generation descent shared by GenerateTasks and
// ExtendTask: on a leaf it writes the canonical class placement into the sink
// with the group's weight (specs/searcher.md). Before the write the forced-
// chain gate (ShouldPruneState) drops classes with zero completions — pruned
// leaves pay the check but not the canonicalization, and one hook covers both
// tables. It is not callback-unified with dfsCount: recursion with a function
// parameter never inlines, and the base case is the only difference —
// duplicating the loop is cheaper.
func (s *Searcher) dfsTask(ctx context.Context, st state.State, end, depth int, weight uint64, c cache.Sink, res *types.Result) {
	if ctx.Err() != nil {
		return
	}

	if st.CountBits() >= depth {
		todo := st.Invert(s.graph.GetTotalCells())
		if pruned, reason := s.pruner.ShouldPruneState(end, todo); pruned {
			res.CountPrune(reason)
			return
		}
		K, rep, _ := s.sym.CanonicalClass(st, end) // orbitSize is the reader's
		c.Set(K, rep, weight)
		res.CacheWrites++
		return
	}

	unvisited := st.Invert(s.graph.GetTotalCells())
	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

	for n := range cand.AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() {
			if pruned, reason := s.pruner.ShouldPruneAfterVisit(n, newUnvisited); pruned {
				res.CountPrune(reason)
				continue
			}
		}
		s.dfsTask(ctx, st.Visit(n), n, depth, weight, c, res)
	}
}

// dfsCount is the reversal count-DFS: full descent (a full board counts 1)
// unless stopLevel ≥ 0 and the visit count hits it exactly, where completions
// answers through the task cache. Steps grow bits one at a time, so == suffices.
func (s *Searcher) dfsCount(ctx context.Context, st state.State, end, stopLevel int, c *cache.View, res *types.Result) int {
	if ctx.Err() != nil {
		return 0
	}

	total := s.graph.GetTotalCells()
	bits := st.CountBits()
	if bits >= total {
		return 1
	}

	unvisited := st.Invert(total)
	if stopLevel >= 0 && bits == stopLevel {
		return s.completions(unvisited, end, c, res)
	}

	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

	found := 0
	for n := range cand.AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() {
			if pruned, reason := s.pruner.ShouldPruneAfterVisit(n, newUnvisited); pruned {
				res.CountPrune(reason)
				continue
			}
		}
		found += s.dfsCount(ctx, st.Visit(n), n, stopLevel, c, res)
	}
	return found
}

// completions answers f(T,end) at the stop level for U = unvisited: every
// completion of (T,t) reverses to a suffix covering exactly U and ending at a
// neighbor u of t, so f(T,t) = Σ_{u∈U, u~t} h(U,u). The canonical frame is
// computed once per mask — all ends of this remainder share one K — and the
// memo takes exactly one Get(K) per call (specs/searcher.md, plan 18). Each
// candidate u moves into K's coordinates through the frame (TransformCell) and
// collapses to its class tag (ClassRep); the answer is the histogram slot
// Weight(rep) divided by CellOrbitSize — exact, because a class weight is a
// sum of orbit sizes of that class (generalization of ADR-011 from pairs to
// classes). An absent mask answers every probe as a miss; an absent class
// misses just that probe. Writer and reader call the same symmetry functions,
// so canonicalization agrees by construction. Every probe is attributed to res
// without atomics (one Result per worker).
func (s *Searcher) completions(unvisited state.State, end int, c *cache.View, res *types.Result) int {
	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)
	K, frame, stab := s.sym.CanonicalMaskFrame(unvisited)
	val, maskFound := c.Get(K)

	found := 0
	for u := range cand.AllVisited() {
		if !maskFound {
			res.CacheMisses++
			continue
		}
		rep := s.sym.ClassRep(stab, s.sym.TransformCell(frame, int(u)))
		weight, ok := val.Weight(rep)
		if !ok {
			res.CacheMisses++
			continue
		}
		res.CacheHits++
		found += int(weight / uint64(s.sym.CellOrbitSize(stab, int(rep))))
	}
	return found
}
