package counter

import (
	"context"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/path"
	"knighttour/searcher"
	"knighttour/symmetry"
	"knighttour/types"
)

const DefaultPrecomputeDepth = 0

type Counter struct {
	cache    *cache.Cache
	graph    *graph.Graph
	symmetry *symmetry.Symmetry
	searcher *searcher.Searcher
}

func NewCounter(graph *graph.Graph) *Counter {
	size := graph.Size()
	sym := symmetry.NewSymmetry(size)
	searcherObj := searcher.NewSearcher(graph, sym)
	return &Counter{
		graph:    graph,
		symmetry: sym,
		searcher: searcherObj,
	}
}

func (c *Counter) CountFromPosition(ctx context.Context, start int) int {
	result := c.searcher.CountPaths(ctx, start)
	return result.TotalPathsFound
}

func (c *Counter) ParallelCount(ctx context.Context, monitor monitoring.Monitor, workers int) uint64 {
	return c.ParallelCountWithDepth(ctx, monitor, workers, DefaultPrecomputeDepth)
}

func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers int, precomputeDepth int) uint64 {
	groups := c.symmetry.GetCanonicalGroups()

	var allSubtasks []types.Subtask
	for _, group := range groups {
		p := group.Canonical
		subtasks := c.searcher.GenerateSubtasksWithMetadata(ctx, p, group.OrbitSize, precomputeDepth)
		allSubtasks = append(allSubtasks, subtasks...)
	}

	monitor.AddTasks(allSubtasks...)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	total := atomic.Uint64{}
	for _, task := range allSubtasks {
		g.Go(func() error {
			p := path.New(task.State, task.Start, task.End)
			result := c.searcher.CountPathsDFS(ctx, p)

			total.Add(uint64(result.TotalPathsFound * task.SymmetriesCount))

			monitor.RecordTaskCompletion(task, result)
			return nil
		})
	}

	_ = g.Wait()

	return total.Load()
}
