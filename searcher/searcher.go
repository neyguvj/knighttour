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
	result := s.CountPathsDFS(ctx, p)
	return result
}

func (s *Searcher) GenerateSubtasks(ctx context.Context, cache *cache.Cache, start int, orbetSize int, depth int) (result types.Result) {
	startPath := path.New(state.NewState(start), start, start)
	s.countPathsDFS(ctx, startPath, func(p path.Path) bool {
		if s.graph.SholdSkip(p.Start()) {
			return true
		}
		if depth == 0 {
			cache.Set(p, 1)
			result.CachedPaths++
			return true
		}
		if p.State().CountBits() >= depth {
			cache.Set(p, 1)
			result.CachedPaths++
			return true
		}
		return false
	})
	return result
}

func (s *Searcher) CountPathsDFS(ctx context.Context, p path.Path) (result types.Result) {
	totalCells := s.graph.GetTotalCells()

	s.countPathsDFS(ctx, p, func(p path.Path) bool {
		if p.State().IsFull(totalCells) {
			result.TotalPathsFound++
			return true
		}

		return false
	})
	return result
}

func (s *Searcher) countPathsDFS(ctx context.Context, p path.Path, onResult func(path.Path) (stop bool)) {
	if ctx.Err() != nil {
		return
	}

	if onResult(p) {
		return
	}

	neighbors := s.graph.GetNeighbors(p.End())
	for _, neighbor := range neighbors {
		if p.State().IsVisited(neighbor) {
			continue
		}

		newState := p.State().Visit(neighbor)
		newPos := path.New(newState, p.Start(), neighbor)

		if s.deadend.ShouldPrune(newPos) {
			continue
		}

		s.countPathsDFS(ctx, newPos, onResult)
	}
}
