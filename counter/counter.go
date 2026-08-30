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

const DefaultPrecomputeDepth = 1

type Counter struct {
	graph    *graph.Graph
	symmetry *symmetry.Symmetry
	searcher *searcher.Searcher
}

func NewCounter(g *graph.Graph) *Counter {
	size := g.Size()
	sym := symmetry.NewSymmetry(size)
	searcherObj := searcher.NewSearcher(g, sym)
	return &Counter{
		graph:    g,
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

func (c *Counter) generateSubTasks(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) *cache.Cache {
	taskCache := cache.NewCache(c.symmetry)
	groups := c.symmetry.GetCanonicalGroups()
	monitor.AddTasks(len(groups))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for _, group := range groups {
		p := group.Canonical
		g.Go(func() error {
			result := c.searcher.GenerateSubtasks(ctx, taskCache, p, group.OrbitSize, precomputeDepth)
			monitor.ReportPathsCached(result.CachedPaths)
			monitor.ReportTaskCompleted()
			return nil
		})
	}
	_ = g.Wait()

	return taskCache
}

func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) uint64 {
	taskCache := c.generateSubTasks(ctx, monitor, workers, precomputeDepth)
	monitor.AddTasks(taskCache.ItemsCount())

	total := atomic.Uint64{}
	taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, count int) {
		result := c.searcher.CountPathsDFS(ctx, p)
		orbits := uint64(c.symmetry.GetOrbitSize(p.Start()))

		total.Add(uint64(result.TotalPathsFound*count) * orbits)
		monitor.ReportPathsFound(result.TotalPathsFound * count * int(orbits))
		monitor.ReportTaskCompleted()
	})

	return total.Load()
}
