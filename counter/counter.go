package counter

import (
	"context"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/oracle"
	"knighttour/path"
	"knighttour/searcher"
	"knighttour/symmetry"
	"knighttour/types"
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
	return c.ParallelCountWithDepth(ctx, monitor, workers, DefaultPrecomputeDepth, 0)
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
			monitor.ReportSubtask(result)
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
			monitor.ReportSubtask(result)
			monitor.ReportTaskCompleted()
			return nil
		})
	}
	_ = g.Wait()

	return taskCache
}

// ParallelCountWithDepth counts all open tours. precomputeDepth controls the
// root/subtask cache (parallelism and dedup). Reversal mode:
//   - oracleDepth > 0: shape-oracle reversal at that mask size — decoupled
//     from generation, works for any level, memory-cheap deep tails
//     (specs/oracle.md); costs h recomputation per class.
//   - oracleDepth == 0 (default): legacy prefix-cache reversal when reachable
//     (2·precomputeDepth ≤ totalCells) — fastest, free W/orbitSize lookups;
//     plain descent otherwise, exactly as before the oracle existed.
func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth, oracleDepth int) uint64 {
	taskCache := c.generateSubTasks(ctx, monitor, workers, precomputeDepth)

	var revOracle *oracle.Oracle
	useOracle := oracleDepth > 0
	if useOracle {
		revOracle = oracle.New(c.graph)
	}

	monitor.BeginPhase("counting")
	monitor.AddTasks(taskCache.ItemsCount())

	total := atomic.Uint64{}
	taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, weight int) error {
		// Counting reads the oracle concurrently; generation is over and the
		// task cache has no writers here (duplicate oracle computes are benign).
		var result types.Result
		if useOracle {
			result = c.searcher.CountPathsWithReversal(ctx, p, revOracle, oracleDepth)
		} else {
			result = c.searcher.CountPathsWithCacheReversal(ctx, p, taskCache, precomputeDepth)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		paths := uint64(result.TotalPathsFound) * uint64(weight)
		total.Add(paths)
		monitor.ReportPathsFound(int(paths))
		monitor.ReportSubtask(result) // reversal hits/misses + pruning of the counting phase
		monitor.ReportTaskCompleted()
		return nil
	})

	if useOracle {
		lookups, computes, classes, zeros := revOracle.Stats()
		monitor.ReportOracleStats(int(lookups), int(computes), int(classes), int(zeros))
	}

	return total.Load()
}
