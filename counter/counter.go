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
)

const DefaultPrecomputeDepth = 0

type Counter struct {
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

func (c *Counter) generateSubTasks(ctx context.Context, monitor monitoring.Monitor, workers int, precomputeDepth int) *cache.Cache {
	cache := cache.NewCache(c.symmetry)
	groups := c.symmetry.GetCanonicalGroups()
	monitor.AddTasks(len(groups))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	g.Go(func() error {
		for _, group := range groups {
			p := group.Canonical
			result := c.searcher.GenerateSubtasks(ctx, cache, p, group.OrbitSize, precomputeDepth)
			monitor.ReportPathsCached(result.CachedPaths)
			monitor.ReportTaskCompleted()
		}
		return nil
	})
	_ = g.Wait()

	return cache
}

func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers int, precomputeDepth int) uint64 {
	taskCache := c.generateSubTasks(ctx, monitor, workers, precomputeDepth)
	monitor.AddTasks(taskCache.ItemsCount())

	total := atomic.Uint64{}
	taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, count int) {
		result := c.searcher.CountPathsDFS(ctx, p)
		group := c.symmetry.GetCanonicalGroupByPosition(p.Start())

		total.Add(uint64(result.TotalPathsFound * count * group.OrbitSize))
		monitor.ReportPathsFound(result.TotalPathsFound * count * group.OrbitSize)
		monitor.ReportTaskCompleted()
	})

	return total.Load()
}
