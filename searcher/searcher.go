package searcher

import (
	"context"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/oracle"
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

// Reversal enables early termination of the counting DFS: once only N cells
// are left unvisited, completions are answered instead of descending further.
// Sound because every completion of (T, t) reverses to a suffix covering
// exactly U = full &^ T and ending at a neighbor of t, so
// f(T,t) = Σ_{u∈U, u~t} h(U,u).
//
// It owns the whole reverse-path counting logic: dfs only checks the stop
// level and delegates to Completions. Fields are unexported; instances come
// from newOracleReversal/newCacheReversal (nil result disables the mode —
// parameter validation lives in those constructors).
//
// Exactly one source is set:
//   - Oracle serves true h(U,u) memoized by translation+D4 shape class
//     (h depends only on the induced subgraph of U with a marked end); works
//     for any level, no generation coupling (oracle.md).
//   - Cache is the legacy fast path: h = W(canon)/orbitSize from the prefix
//     cache built by generation at exactly this depth — cheaper (generation
//     already computed the counts) but requires 2d ≤ totalCells.
type Reversal struct {
	graph     *graph.Graph
	sym       *symmetry.Symmetry
	oracle    *oracle.Oracle
	cache     *cache.Cache
	stopLevel int // count-DFS stops where CountBits(st) == stopLevel
}

// newOracleReversal builds an oracle-backed reversal for counting paths that
// already cover minBits cells, stopping at level totalCells-oracleDepth.
// Returns nil (plain full DFS) when the source or level is unusable:
// o == nil, oracleDepth outside [1, totalCells), or stop above p's own depth.
func newOracleReversal(g *graph.Graph, sym *symmetry.Symmetry, o *oracle.Oracle, oracleDepth, minBits int) *Reversal {
	total := g.GetTotalCells()
	if o == nil || oracleDepth < 1 || oracleDepth >= total {
		return nil
	}
	stop := total - oracleDepth
	if stop < minBits {
		return nil
	}
	return &Reversal{graph: g, sym: sym, oracle: o, stopLevel: stop}
}

// newCacheReversal builds the legacy prefix-cache reversal stopping at level
// totalCells-d. Returns nil (plain full DFS) when c == nil or 2d > totalCells.
func newCacheReversal(g *graph.Graph, sym *symmetry.Symmetry, c *cache.Cache, d int) *Reversal {
	total := g.GetTotalCells()
	if c == nil || 2*d > total {
		return nil
	}
	return &Reversal{graph: g, sym: sym, cache: c, stopLevel: total - d}
}

// Completions answers f(T,end) at the stop level for U = unvisited: the sum of
// h(U,u) over neighbors u ∈ U. The source is picked by a nil-check (no
// interface dispatch on this hot lookup): oracle normalizes the mask once per
// node via Prepare/GetPrepared; the legacy cache path transforms the mask once
// and reads W(canon)/orbitSize per candidate end (missing entries → 0).
func (r *Reversal) Completions(unvisited state.State, end int) int {
	cand := r.graph.GetNeighborMask(end).Intersect(unvisited)

	if r.oracle != nil {
		var sc oracle.ShapeCtx
		r.oracle.Prepare(unvisited, &sc)

		found := 0
		for u := range cand.AllVisited() {
			found += int(r.oracle.GetPrepared(&sc, int(u)))
		}
		return found
	}

	states := r.sym.TransformStates(unvisited)

	found := 0
	for u := range cand.AllVisited() {
		canonical, orbitSize := r.sym.CanonicalFromStates(states, int(u))
		weight, ok := r.cache.GetCanonical(canonical)
		if !ok {
			continue
		}
		found += weight / orbitSize
	}
	return found
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

// CountPathsWithReversal counts full completions of p with oracle-based early
// stop at level totalCells-oracleDepth (plain full search when o == nil or the
// level is below p's own depth).
func (s *Searcher) CountPathsWithReversal(ctx context.Context, p path.Path, o *oracle.Oracle, oracleDepth int) (result types.Result) {
	rev := newOracleReversal(s.graph, s.sym, o, oracleDepth, p.State().CountBits())
	return s.countWithRev(ctx, p, rev)
}

// CountPathsWithCacheReversal counts full completions of p with the legacy
// prefix-cache reversal at level totalCells-d (plain full search when c == nil
// or 2d > totalCells).
func (s *Searcher) CountPathsWithCacheReversal(ctx context.Context, p path.Path, c *cache.Cache, d int) (result types.Result) {
	rev := newCacheReversal(s.graph, s.sym, c, d)
	return s.countWithRev(ctx, p, rev)
}

func (s *Searcher) countWithRev(ctx context.Context, p path.Path, rev *Reversal) (result types.Result) {
	var stats dfsStats
	result.TotalPathsFound = s.dfs(ctx, p.State(), p.End(), s.graph.GetTotalCells(), nil, 0, &stats, rev)
	result.Pruned = stats.pruned
	return result
}

// dfs is the unified hot DFS. When c != nil it stops at depth and stores each
// prefix as (state, end) with the given orbit weight; otherwise it counts full
// completions down to a full board. When rev != nil counting stops early at
// level rev.stopLevel delegating to Reversal.Completions. stats aggregates
// cache writes and pruned branches for monitoring.
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

	if rev != nil && bits == rev.stopLevel {
		return rev.Completions(unvisited, end)
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
