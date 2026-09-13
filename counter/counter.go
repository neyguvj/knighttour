package counter

import (
	"context"
	"errors"
	"runtime/debug"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/path"
	"knighttour/searcher"
	"knighttour/symmetry"
)

// TwoPhaseBaseDepth is the intermediate accumulator depth of generation phase A.
// Phase A stays single-pass (parallel over canonical start groups — enough for
// its tiny trees); phase B extends every intermediate entry independently to
// the target depth, exposing thousands of tasks instead of ~10 groups. When
// precomputeDepth ≤ it, phase B degenerates: each intermediate entry writes
// itself into the task-cache.
const TwoPhaseBaseDepth = 5

// DefaultGCPercentReversal is the GOGC the counting pipeline runs under for its
// whole duration (ADR-014): the task-cache peak is the process high-water mark
// and headroom above it is expensive — measured on 7×7 d22 (specs/decisions).
const DefaultGCPercentReversal = 40

// defaultPrecomputeDepths is the per-board default split depth: measured
// optima on 5×5/6×6 sweeps, a sweep-tuned value on 7×7 and a conservative one
// on 8×8 (deeper splits grow the task-cache exponentially and can exhaust
// memory). main.go applies it when -precompute-depth is not set. The values
// await the reversal resweep of ADR-016.
var defaultPrecomputeDepths = map[int]int{5: 6, 6: 10, 7: 20, 8: 14}

// DefaultPrecomputeDepth returns the recommended precompute depth for a board
// size (the table above; falls back to TwoPhaseBaseDepth+1 for unknown sizes).
func DefaultPrecomputeDepth(size int) int {
	if d, ok := defaultPrecomputeDepths[size]; ok {
		return d
	}
	return TwoPhaseBaseDepth + 1
}

// Counter orchestrates counting runs over one graph: each ParallelCount* call
// executes the single gen A → gen B → count(reversal) pipeline
// (specs/counter.md, ADR-016).
type Counter struct {
	graph     *graph.Graph
	symmetry  *symmetry.Symmetry
	searcher  *searcher.Searcher
	gcPercent int // GOGC for the pipeline duration (ADR-014); 0 = untouched
}

// SetGCPercent sets the GC percent applied for the duration of the counting
// pipeline (ADR-014): p > 0 calls debug.SetGCPercent on entry and restores the
// previous value on exit; p <= 0 leaves the runtime GC untouched (the CLI
// rejects negatives, so only 0 means "don't touch"). Default
// DefaultGCPercentReversal. Call before counting.
func (c *Counter) SetGCPercent(p int) { c.gcPercent = p }

// NewCounter returns a counter for g running the single reversal pipeline
// (specs/counter.md, ADR-016).
func NewCounter(g *graph.Graph) *Counter {
	size := g.Size()
	sym := symmetry.NewSymmetry(size)
	searcherObj := searcher.NewSearcher(g, sym)
	return &Counter{
		graph:     g,
		symmetry:  sym,
		searcher:  searcherObj,
		gcPercent: DefaultGCPercentReversal,
	}
}

// ParallelCount counts all open tours with the default split depth for the
// board size.
func (c *Counter) ParallelCount(ctx context.Context, monitor monitoring.Monitor, workers int) uint64 {
	return c.ParallelCountWithDepth(ctx, monitor, workers, DefaultPrecomputeDepth(c.graph.Size()))
}

// ParallelCountWithDepth counts all open tours with the single pipeline
// (specs/counter.md, ADR-016): gen A over canonical start groups into the
// intermediate accumulator, gen B chunk workers extending each intermediate
// entry into the live task-cache (direct Set — no LocalSink: the record
// profile differs from the intermediate table's and hits are read in the same
// run they are written), and counting walking the task-cache directly under
// shard read locks (Cache.Each) with the early-stop count-DFS — total =
// Σ W(task)·f(task). precomputeDepth is the meet-in-the-middle split
// (validated ≤ size²/2 by main.go); deeper than that duplicates the dual cut
// of the reversed tour. The whole run executes under the configured GC percent
// (SetGCPercent, ADR-014).
func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) uint64 {
	// Lower GOGC for the entire pipeline (ADR-014): gen B already feeds the
	// task-cache that forms the process peak, so scoping to the count phase
	// would miss it; restore the previous percent on exit. Two concurrent
	// pipelines restore last-writer-wins (specs/counter.md).
	if c.gcPercent > 0 {
		prev := debug.SetGCPercent(c.gcPercent)
		defer debug.SetGCPercent(prev)
	}

	intermediate := c.generateIntermediate(ctx, monitor, workers, precomputeDepth)

	monitor.BeginPhase("gen B")
	monitor.AddTasks(len(intermediate))

	taskCache := cache.NewCache()
	nextEntry := atomic.Int64{}
	// Chunk workers (not g.Go per entry): the atomic index keeps claim
	// contention an order of magnitude below the task count.
	g, gctx := errgroup.WithContext(ctx)
	for range min(len(intermediate), workers) {
		g.Go(func() error {
			for {
				i := int(nextEntry.Add(1)) - 1
				if i >= len(intermediate) {
					return nil
				}
				select {
				case <-gctx.Done():
					return nil
				default:
				}
				e := intermediate[i]
				result := c.searcher.ExtendTask(gctx, taskCache, e.Path, e.Weight, precomputeDepth)
				monitor.ReportSubtask(&result)
				monitor.ReportTaskCompleted()
			}
		})
	}
	_ = g.Wait()

	return c.countTasks(ctx, monitor, workers, taskCache, precomputeDepth)
}

// countTasks is the reversal counting phase (specs/counter.md, plan 09): a
// direct Cache.Each walk of the task-cache — zero-allocation dispatch, no
// snapshots. The callback runs the early-stop count-DFS for every task under
// its shard's read lock (no writers after the generation barrier) and adds
// w · f(task) into the shared total. The cache stays alive and read-only until
// the phase ends; it is never drained (specs/cache.md).
func (c *Counter) countTasks(ctx context.Context, monitor monitoring.Monitor, workers int, taskCache *cache.Cache, precomputeDepth int) uint64 {
	monitor.BeginPhase("counting")
	monitor.AddTasks(taskCache.ItemsCount())

	var total atomic.Uint64
	// Cancellation just leaves the partial total — the same semantics as the
	// shard-cursor loop this walk replaced. Any other error is unreachable per
	// contract (the callback never fails, specs/cache.md): it would mean a bug
	// in Each, so fail loudly instead of returning a silently partial result.
	if err := taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, weight uint64) error {
		c.countOneTask(ctx, monitor, taskCache, precomputeDepth, p, weight, &total)
		return nil
	}); err != nil && !errors.Is(err, context.Canceled) {
		panic("counter: unexpected Cache.Each error: " + err.Error())
	}

	return total.Load()
}

// countOneTask runs the early-stop count-DFS for a single task and folds its
// weighted path count and per-task statistics into the shared counters. It is
// invoked from Cache.Each under the task's shard read lock.
func (c *Counter) countOneTask(ctx context.Context, monitor monitoring.Monitor, taskCache *cache.Cache, precomputeDepth int, p path.Path, weight uint64, total *atomic.Uint64) {
	if ctx.Err() != nil {
		return
	}
	result := c.searcher.CountPathsWithCacheReversal(ctx, p, taskCache, precomputeDepth)
	paths := uint64(result.TotalPathsFound) * weight
	total.Add(paths)
	monitor.ReportPathsFound(int(paths))
	monitor.ReportSubtask(&result)
	monitor.ReportTaskCompleted()
}

// generateIntermediate runs phase A: DFS from every canonical start group to
// base = min(precomputeDepth, TwoPhaseBaseDepth), accumulating D4-canonical
// prefixes into the intermediate table through per-group LocalSinks. The
// snapshot is the independent-task worklist of phase B.
func (c *Counter) generateIntermediate(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) []cache.Entry {
	monitor.BeginPhase("gen A")
	intermediate := cache.NewAccumulator()
	groups := c.symmetry.GetCanonicalGroups()
	monitor.AddTasks(len(groups))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for _, group := range groups {
		p := group.Canonical
		g.Go(func() error {
			sink := intermediate.Local()
			defer sink.Flush()
			base := min(precomputeDepth, TwoPhaseBaseDepth)
			result := c.searcher.GenerateRoots(ctx, sink, p, uint64(group.OrbitSize), base)
			monitor.ReportSubtask(&result)
			monitor.ReportTaskCompleted()
			return nil
		})
	}
	_ = g.Wait()

	return intermediate.Drain()
}
