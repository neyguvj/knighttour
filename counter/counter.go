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

const DefaultPrecomputeDepth = 5

// TwoPhaseBaseDepth is the intermediate cache depth for two-phase subtask
// generation. When precomputeDepth exceeds it, generation runs in two phases:
// phase A fills a small intermediate cache at this depth (parallel over start
// groups), phase B extends every intermediate entry to the target depth
// (parallel over thousands of entries). Below the threshold the number of
// canonical starts provides enough parallelism and single-phase is used.
const TwoPhaseBaseDepth = 5

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

// generateSubTasks builds the task cache for precomputeDepth, picking the
// strategy by depth: single-phase (parallel over canonical start groups) up to
// TwoPhaseBaseDepth, two-phase above it. Monitoring phases: "generation" for
// single-phase, "gen A"/"gen B" for two-phase.
func (c *Counter) generateSubTasks(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) *cache.Cache {
	if precomputeDepth > TwoPhaseBaseDepth {
		return c.generateSubTasksTwoPhase(ctx, monitor, workers, precomputeDepth)
	}
	return c.generateSubTasksSinglePhase(ctx, monitor, "generation", workers, precomputeDepth)
}

func (c *Counter) generateSubTasksSinglePhase(ctx context.Context, monitor monitoring.Monitor, phase string, workers, precomputeDepth int) *cache.Cache {
	monitor.BeginPhase(phase)
	taskCache := cache.NewCache(c.symmetry)
	groups := c.symmetry.GetCanonicalGroups()
	monitor.AddTasks(len(groups))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for _, group := range groups {
		p := group.Canonical
		g.Go(func() error {
			result := c.searcher.GenerateSubtasks(ctx, taskCache, p, group.OrbitSize, precomputeDepth)
			monitor.ReportCacheWrites(result.CacheWrites)
			monitor.ReportPruned(result.Pruned)
			monitor.ReportTaskCompleted()
			return nil
		})
	}
	_ = g.Wait()

	return taskCache
}

// generateSubTasksTwoPhase splits generation in two phases to expose more
// parallelism than the ~10 canonical start groups provide: phase A fills an
// intermediate cache at TwoPhaseBaseDepth, phase B extends every intermediate
// entry independently to precomputeDepth. The resulting cache is identical to
// single-phase generation (see searcher.ExtendSubtask).
func (c *Counter) generateSubTasksTwoPhase(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) *cache.Cache {
	intermediate := c.generateSubTasksSinglePhase(ctx, monitor, "gen A", workers, TwoPhaseBaseDepth)
	entries := intermediate.Entries()

	monitor.BeginPhase("gen B")
	monitor.AddTasks(len(entries))

	taskCache := cache.NewCache(c.symmetry)
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for _, e := range entries {
		g.Go(func() error {
			result := c.searcher.ExtendSubtask(ctx, taskCache, e.Path, e.Weight, precomputeDepth)
			monitor.ReportCacheWrites(result.CacheWrites)
			monitor.ReportPruned(result.Pruned)
			monitor.ReportTaskCompleted()
			return nil
		})
	}
	_ = g.Wait()

	return taskCache
}

func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) uint64 {
	taskCache := c.generateSubTasks(ctx, monitor, workers, precomputeDepth)

	monitor.BeginPhase("counting")
	monitor.AddTasks(taskCache.ItemsCount())

	total := atomic.Uint64{}
	taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, weight int) {
		// The task cache doubles as the reversal lookup table: when only
		// precomputeDepth cells remain, completions are answered from it
		// instead of descending (no-op unless 2*precomputeDepth <= totalCells).
		// Safe for concurrent reads inside Each because generation is over:
		// the cache is strictly read-only during this phase.
		result := c.searcher.CountPathsWithReversal(ctx, p, taskCache, precomputeDepth)

		paths := uint64(result.TotalPathsFound) * uint64(weight)
		total.Add(paths)
		monitor.ReportPathsFound(int(paths))
		monitor.ReportPruned(result.Pruned)
		monitor.ReportTaskCompleted()
	})

	return total.Load()
}
