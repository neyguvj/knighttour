package searcher

import (
	"context"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/path"
	"knighttour/pruner"
	"knighttour/state"
	"knighttour/symmetry"
	"knighttour/types"
)

// Searcher is the pure traversal layer (specs/searcher.md): DFS backtracking
// over bitmasks with pruning plus the generation phases and the reversal
// count-DFS. It owns no memo tables — the caches belong to the calling loop.
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
// from a canonical start down to depth, writing each leaf prefix straight into
// c as its D4-canonical placement (state, end) with the group's orbit weight —
// both the gen-A intermediate table and the task-cache go this way (ADR-018).
// SholdSkip starts emit nothing. Statistics: CacheWrites counts written
// prefixes.
func (s *Searcher) GenerateTasks(ctx context.Context, c *cache.Cache, start int, orbitSize uint64, depth int) (result types.Result) {
	if s.graph.SholdSkip(start) {
		return result
	}
	s.dfsTask(ctx, state.NewState(start), start, depth, orbitSize, c, &result)
	result.Finalize()
	return result
}

// ExtendTask is phase B of task-cache generation: it continues the task
// descent from an already canonical entry p with its aggregated weight down
// to the target depth. An entry at or beyond the depth (degenerate split,
// precomputeDepth ≤ base) writes itself as-is — descending further would
// lose its record; below the threshold leaves are written canonicalized like
// in GenerateTasks. No SholdSkip check: roots were filtered by phase A.
func (s *Searcher) ExtendTask(ctx context.Context, c *cache.Cache, p path.Path, weight uint64, depth int) (result types.Result) {
	if p.State().CountBits() >= depth {
		c.Set(p, weight)
		result.CacheWrites++
		result.Finalize()
		return result
	}
	s.dfsTask(ctx, p.State(), p.End(), depth, weight, c, &result)
	result.Finalize()
	return result
}

// CountPathsWithCacheReversal counts the full completions of p with early
// stop at level totalCells−d (specs/searcher.md): at that level the remainder
// U = full \ T is answered by Σ W(canon(U,u))/orbitSize over u ∈ N(t) ∩ U from
// c instead of descending to a full board. c == nil or 2d > totalCells → full
// descent without any cache access (the reversal duality is unreachable).
// Statistics: TotalPathsFound, CacheHits/CacheMisses per lookup, pruning by
// reason.
func (s *Searcher) CountPathsWithCacheReversal(ctx context.Context, p path.Path, c *cache.Cache, d int) (result types.Result) {
	stopLevel := -1 // disabled sentinel: full descent
	if c != nil && 2*d <= s.graph.GetTotalCells() {
		stopLevel = s.graph.GetTotalCells() - d
	}
	result.TotalPathsFound = s.dfsCount(ctx, p.State(), p.End(), stopLevel, c, &result)
	result.Finalize()
	return result
}

// dfsTask is the single generation descent shared by GenerateTasks and
// ExtendTask: on a leaf it writes the canonical placement into the cache with
// the group's weight (specs/searcher.md). It is not callback-unified with
// dfsCount: recursion with a function parameter never inlines, and the base
// case is the only difference — duplicating the loop is cheaper.
func (s *Searcher) dfsTask(ctx context.Context, st state.State, end, depth int, weight uint64, c *cache.Cache, res *types.Result) {
	if ctx.Err() != nil {
		return
	}

	if st.CountBits() >= depth {
		c.Set(s.sym.Canonicalize(st, end), weight)
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
// unless stopLevel ≥ 0 and the visit count hits it exactly, where Completions
// answers through the task cache. Steps grow bits one at a time, so == suffices.
func (s *Searcher) dfsCount(ctx context.Context, st state.State, end, stopLevel int, c *cache.Cache, res *types.Result) int {
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
// neighbor u of t, so f(T,t) = Σ_{u∈U, u~t} h(U,u). The task-cache weight of
// canon(U,u) is the sum of orbit sizes over all depth-d prefixes in that key's
// fiber — W = orbitSize·h(U,u) — hence the exact division (specs/searcher.md,
// ADR-011). Missing entries contribute 0 (no such prefix ⇒ h == 0); every
// lookup is attributed to res without atomics (one Result per worker).
func (s *Searcher) completions(unvisited state.State, end int, c *cache.Cache, res *types.Result) int {
	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)
	states := s.sym.TransformStates(unvisited)

	found := 0
	for u := range cand.AllVisited() {
		canonical, orbitSize := s.sym.CanonicalFromStates(states, int(u))
		weight, ok := c.Get(canonical)
		if !ok {
			res.CacheMisses++
			continue
		}
		res.CacheHits++
		found += int(weight / uint64(orbitSize))
	}
	return found
}
