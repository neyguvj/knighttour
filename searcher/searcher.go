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

func (s *Searcher) GenerateSubtasks(ctx context.Context, p path.Path, depth int) []path.Path {
	var subtasks []path.Path

	_ = s.countPathsDFS(ctx, p, func(p path.Path) bool {
		if depth == 0 {
			subtasks = append(subtasks, p)
			return true
		}
		if p.State().CountBits() >= depth {
			subtasks = append(subtasks, p)
			return true
		}
		return false
	})
	return subtasks
}

func (s *Searcher) GenerateSubtasksWithMetadata(ctx context.Context, start int, orbitSize int, depth int) []types.Subtask {
	st := state.State(0).Visit(start)
	p := path.New(st, start, start)

	rawSubtasks := s.GenerateSubtasks(ctx, p, depth)

	canonizedTasks := make(map[path.Path]int)
	for _, task := range rawSubtasks {
		canonicalState := s.sym.CanonicalizePath(task)
		canonizedTasks[canonicalState]++
	}

	var subtasks []types.Subtask

	for task, count := range canonizedTasks {
		subtasks = append(subtasks, types.Subtask{
			State:           task.State(),
			Start:           task.Start(),
			End:             task.End(),
			Depth:           depth,
			SymmetriesCount: orbitSize * count,
		})
	}

	return subtasks
}

func (s *Searcher) CountCenterPaths(ctx context.Context, cache *cache.Cache, p path.Path, SymmetriesCount int) (result types.Result) {
	totalCells := s.graph.GetTotalCells()
	center := totalCells / 2
	return s.countPathsDFS(ctx, p, func(p path.Path) bool {
		if p.Start() == center {
			return true
		}
		if p.End() == center {
			cache.Set(p, SymmetriesCount)
			return true
		}
		return false
	})
}

func (s *Searcher) CountPathsDFS(ctx context.Context, p path.Path) (result types.Result) {
	totalCells := s.graph.GetTotalCells()

	return s.countPathsDFS(ctx, p, func(p path.Path) bool {
		return p.State().IsFull(totalCells)
	})
}

func (s *Searcher) countPathsDFS(ctx context.Context, p path.Path, stopCondition func(path.Path) bool) (result types.Result) {
	if ctx.Err() != nil {
		return types.Result{}
	}

	if stopCondition(p) {
		return types.Result{TotalPathsFound: 1}
	}

	result = types.Result{}

	neighbors := s.graph.GetNeighbors(p.End())
	for _, neighbor := range neighbors {
		if p.State().IsVisited(neighbor) {
			continue
		}

		newState := p.State().Visit(neighbor)
		newPos := path.New(newState, p.Start(), neighbor)

		if s.deadend.ShouldPrune(newPos) {
			result.Pruned++
			continue
		}

		childResult := s.countPathsDFS(ctx, newPos, stopCondition)
		result.Add(childResult)
	}

	return result
}
