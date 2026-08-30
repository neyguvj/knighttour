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
	s.dfs(ctx, st, start, depth, c, orbitSize, &result.CachedPaths)
	return result
}

func (s *Searcher) CountPathsDFS(ctx context.Context, p path.Path) (result types.Result) {
	result.TotalPathsFound = s.dfs(ctx, p.State(), p.End(), s.graph.GetTotalCells(), nil, 0, nil)
	return result
}

// dfs is the unified hot DFS. When c != nil it stops at depth and stores each
// prefix as (state, end) with the given orbit weight; otherwise it counts full
// completions down to a full board.
func (s *Searcher) dfs(ctx context.Context, st state.State, end, depth int, c *cache.Cache, weight int, cached *int) int {
	if ctx.Err() != nil {
		return 0
	}

	if st.CountBits() >= depth {
		if c != nil {
			c.Set(path.New(st, end), weight)
			*cached++
		}
		return 1
	}

	unvisited := st.Invert(s.graph.GetTotalCells())
	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

	found := 0
	for n := range cand.AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() && s.pruner.ShouldPruneAfterVisit(n, newUnvisited) {
			continue
		}
		found += s.dfs(ctx, st.Visit(n), n, depth, c, weight, cached)
	}
	return found
}
