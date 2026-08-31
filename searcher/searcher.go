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

type Searcher struct {
	graph  *graph.Graph
	sym    *symmetry.Symmetry
	pruner *pruner.AdvancedPruner
}

func NewSearcher(g *graph.Graph, sym *symmetry.Symmetry) *Searcher {
	return &Searcher{
		graph:  g,
		sym:    sym,
		pruner: pruner.NewAdvancedPruner(g),
	}
}

// Reversal enables early termination of the counting DFS via the prefix cache:
// once only PrecomputeDepth cells are left unvisited, completions are answered
// from the cache instead of descending further. Sound because every completion
// of (T, t) reverses to a prefix covering exactly U = full &^ T and ending at a
// neighbor of t, so f(T,t) = Σ_{u∈U, u~t} h(U,u), and the cache value W(K) is
// the fiber sum |fiber|·h over the D4 orbit K of (U,u).
type Reversal struct {
	Cache     *cache.Cache
	StopLevel int // totalCells - precomputeDepth; reachable iff 2*precomputeDepth <= totalCells
}

// dfsStats aggregates hot-DFS metrics behind a single pointer so the recursive
// signature stays lean. cacheWrites counts cache.Set calls during prefix
// generation; pruned counts branches cut by ShouldPruneAfterVisit (counted in
// both generation and counting phases).
type dfsStats struct {
	cacheWrites int
	pruned      int
}

func (s *Searcher) CountPaths(ctx context.Context, start int) types.Result {
	st := state.State(0).Visit(start)
	p := path.New(st, start)
	return s.CountPathsDFS(ctx, p)
}

func (s *Searcher) GenerateSubtasks(ctx context.Context, c *cache.Cache, start, orbitSize, depth int) (result types.Result) {
	if s.graph.SholdSkip(start) {
		return result
	}
	st := state.NewState(start)
	var stats dfsStats
	s.dfs(ctx, st, start, depth, c, orbitSize, &stats, nil)
	result.CacheWrites = stats.cacheWrites
	result.Pruned = stats.pruned
	return result
}

// ExtendSubtask continues prefix generation from an already canonical cache
// entry (state, end) down to the target depth, writing leaves into c with the
// entry's aggregated weight. This is phase B of two-phase subtask generation:
// symmetric images of the fiber share continuation trees up to canonicalization
// (graph and pruner are D4-equivariant), so extending the representative once
// with W(entry) reproduces exactly the single-phase cache contents. No
// SholdSkip check here: roots were already filtered by phase A.
func (s *Searcher) ExtendSubtask(ctx context.Context, c *cache.Cache, p path.Path, weight, depth int) (result types.Result) {
	if p.State().CountBits() >= depth {
		return result
	}
	var stats dfsStats
	s.dfs(ctx, p.State(), p.End(), depth, c, weight, &stats, nil)
	result.CacheWrites = stats.cacheWrites
	result.Pruned = stats.pruned
	return result
}

func (s *Searcher) CountPathsDFS(ctx context.Context, p path.Path) types.Result {
	result := s.CountPathsWithReversal(ctx, p, nil, 0)
	return result
}

// CountPathsWithReversal counts full completions of p. When c != nil and the
// reversal level is reachable (2*precomputeDepth <= totalCells), the DFS stops
// as soon as only precomputeDepth cells remain unvisited and answers from the
// cache via the reversal bijection; otherwise it is a plain full search.
func (s *Searcher) CountPathsWithReversal(ctx context.Context, p path.Path, c *cache.Cache, precomputeDepth int) (result types.Result) {
	var rev *Reversal
	if total := s.graph.GetTotalCells(); c != nil && 2*precomputeDepth <= total {
		rev = &Reversal{Cache: c, StopLevel: total - precomputeDepth}
	}
	var stats dfsStats
	result.TotalPathsFound = s.dfs(ctx, p.State(), p.End(), s.graph.GetTotalCells(), nil, 0, &stats, rev)
	result.Pruned = stats.pruned
	return result
}

// dfs is the unified hot DFS. When c != nil it stops at depth and stores each
// prefix as (state, end) with the given orbit weight; otherwise it counts full
// completions down to a full board. When rev != nil counting stops early at
// level totalCells-rev.PrecomputeDepth using the reversal cache lookup. stats
// aggregates cache writes and pruned branches for monitoring.
func (s *Searcher) dfs(ctx context.Context, st state.State, end, depth int, c *cache.Cache, weight int, stats *dfsStats, rev *Reversal) int {
	if ctx.Err() != nil {
		return 0
	}

	bits := st.CountBits()
	if bits >= depth {
		if c != nil {
			c.Set(path.New(st, end), weight)
			stats.cacheWrites++
		}
		return 1
	}

	unvisited := st.Invert(s.graph.GetTotalCells())

	if rev != nil && bits == rev.StopLevel {
		return s.completionsFromCache(rev.Cache, unvisited, end)
	}

	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

	found := 0
	for n := range cand.AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() && s.pruner.ShouldPruneAfterVisit(n, newUnvisited) {
			stats.pruned++
			continue
		}
		found += s.dfs(ctx, st.Visit(n), n, depth, c, weight, stats, rev)
	}
	return found
}

// completionsFromCache answers f(T,end) for |U| == PrecomputeDepth:
// Σ over neighbors u in U of h(U,u), with h recovered from the cache as
// W(canon)/orbitSize (exact division; missing entries contribute 0).
func (s *Searcher) completionsFromCache(c *cache.Cache, unvisited state.State, end int) int {
	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

	// All candidate ends share one mask: transform it once per node.
	states := s.sym.TransformStates(unvisited)

	found := 0
	for u := range cand.AllVisited() {
		canonical, orbitSize := s.sym.CanonicalFromStates(states, int(u))
		weight, ok := c.GetCanonical(canonical)
		if !ok {
			continue
		}
		found += weight / orbitSize
	}
	return found
}
