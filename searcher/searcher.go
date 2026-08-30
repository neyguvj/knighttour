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
	graph   *graph.Graph
	sym     *symmetry.Symmetry
	deadend *pruner.DeadEndPruner
}

func NewSearcher(graph *graph.Graph, sym *symmetry.Symmetry) *Searcher {
	return &Searcher{
		graph:   graph,
		sym:     sym,
		deadend: pruner.NewDeadEndPruner(graph),
	}
}

func (s *Searcher) CountPaths(ctx context.Context, start int) types.Result {
	st := state.State(0).Visit(start)
	p := path.New(st, start, start)
	return s.CountPathsDFS(ctx, p)
}

func (s *Searcher) GenerateSubtasks(ctx context.Context, c *cache.Cache, start int, orbetSize int, depth int) (result types.Result) {
	if s.graph.SholdSkip(start) {
		return result
	}
	st := state.NewState(start)
	s.dfs(ctx, st, start, start, depth, c, &result.CachedPaths)
	return result
}

func (s *Searcher) CountPathsDFS(ctx context.Context, p path.Path) (result types.Result) {
	result.TotalPathsFound = s.dfs(ctx, p.State(), p.Start(), p.End(), s.graph.GetTotalCells(), nil, nil)
	return result
}

func (s *Searcher) dfs(ctx context.Context, st state.State, start, end, depth int, c *cache.Cache, cached *int) int {
	if ctx.Err() != nil {
		return 0
	}

	if st.CountBits() >= depth {
		if c != nil {
			c.Set(path.New(st, start, end), 1)
			*cached++
		}
		return 1
	}

	unvisited := st.Invert(s.graph.GetTotalCells())
	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

	found := 0
	for n := range cand.AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() && s.deadend.ShouldPruneAfterVisit(n, newUnvisited) {
			continue
		}
		found += s.dfs(ctx, st.Visit(n), start, n, depth, c, cached)
	}
	return found
}
