package counter

import (
	"context"
	"iter"
	"runtime/debug"
	"sync/atomic"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/path"
	"knighttour/searcher"
	"knighttour/symmetry"
	"knighttour/workerpool"
)

// TwoPhaseBaseDepth is the intermediate table depth of generation phase A.
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

// dispatchCapacityPerConsumer is the per-consumer record depth K of the
// counting-phase batch channel: the channel capacity in records is
// max(min(consumers×K, records), 1) and its buffer ceil(capacity/Beff) slots
// — one allocation for the phase, zero allocations per record. Fixed at the
// measured best point of the plan-13 sweep on 7×7 d22 (window [1000..10000],
// ADR-019; value frozen by plan 16); there is no runtime override (ADR-020).
const dispatchCapacityPerConsumer = 10000

// dispatchClaimBatch is the batch ceiling B of plan 13: the effective batch
// Beff comes from effectiveBatch (per-record on small tables, saturating to
// B), one channel operation per Beff records on each side. It also sizes the
// entryBatch payload, so Beff ≤ B always holds. Fixed at the measured optimum
// (ADR-019; ADR-010's claim batch was 16 too).
const dispatchClaimBatch = 16

// stagingFlushLimit is the per-worker auto-flush threshold of the gen-B
// batched writer (plan 15): one Lock per touched shard per F emissions
// instead of per leaf, cutting the lock wake/spin churn measured to dominate
// cache.Set on 7×7 d22. Budget: 24 B × F per worker ≈ 196 KB, a few
// MB across workers — noise against the task-cache peak. Fixed value, no
// runtime override (ADR-020).
const stagingFlushLimit = 8192

// dispatchGranularityC is the constant C of effectiveBatch: the lower bound
// on the number of claims per consumer once the formula is active — while
// records ≤ consumers·C the phase delivers per record (plan 13 fix). Fixed
// with the mechanics measurement; no runtime override (ADR-020).
const dispatchGranularityC = 4

// defaultPrecomputeDepths is the per-board default split depth: the global
// minimum of wall time on each board's measured depth window (ADR-017 — peak
// RSS is a reference metric, not a gate). 8×8 keeps an unmeasured placeholder
// (ADR-015). main.go applies it when -precompute-depth is not set.
var defaultPrecomputeDepths = map[int]int{5: 6, 6: 14, 7: 22, 8: 14}

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
// (specs/counter.md, ADR-016): a thin orchestrator over generateIntermediate
// (gen A), extendTasks (gen B into the task-cache) and countTasks (reversal
// counting, total = Σ W(task)·f(task)). precomputeDepth is the
// meet-in-the-middle split (validated ≤ size²/2 by main.go); deeper than that
// duplicates the dual cut of the reversed tour. The whole run executes under
// the configured GC percent (SetGCPercent, ADR-014).
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
	taskCache := c.extendTasks(ctx, monitor, workers, intermediate, precomputeDepth)
	return c.countTasks(ctx, monitor, workers, taskCache, precomputeDepth, dispatchCapacityPerConsumer)
}

// extendTasks runs phase B (specs/counter.md): a workerpool.Run crew extends
// every intermediate entry to the target depth and writes leaves into a fresh
// task-cache through a private Staging each (batched writes, ADR-031). The
// returned table is not yet sealed — the caller stops all writers via the Run
// barrier before counting.
func (c *Counter) extendTasks(ctx context.Context, monitor monitoring.Monitor, workers int, intermediate []cache.Entry, precomputeDepth int) *cache.Cache {
	monitor.BeginPhase("gen B")
	monitor.AddTasks(len(intermediate))

	taskCache := cache.NewCache()
	// Chunk workers claiming indices from one atomic cursor: claim contention
	// stays an order of magnitude below the task count (ADR-031).
	workerpool.New(workers).Run(ctx, len(intermediate), func(ctx context.Context, idx iter.Seq[int]) {
		// One private Staging per worker (plan 15); the deferred Flush runs on
		// every exit path — exhausted worklist or cancelled ctx — so buffered
		// leaves are no less visible than direct writes.
		staging := taskCache.NewStaging(stagingFlushLimit)
		defer staging.Flush()
		for i := range idx {
			e := intermediate[i]
			result := c.searcher.ExtendTask(ctx, staging, e.Path, e.Weight, precomputeDepth)
			monitor.ReportSubtask(&result)
			monitor.ReportTaskCompleted()
		}
	})

	return taskCache
}

// effectiveBatch computes the phase's single dispatch granularity Beff once at
// phase start from View.Len(): min(B, max(1, records/(consumers·C))) —
// per-record delivery while records ≤ consumers·C (small tables never pile up
// on one consumer), saturating to the ceiling B once records ≥ B·consumers·C
// (specs/counter.md). The producer fills batches of exactly Beff records. The
// knobs are explicit parameters: production passes the fixed defaults, tests
// pin the formula at other knobs without mutating package state.
func effectiveBatch(records, consumers, claimB, granularityC int) int {
	ceiling := max(claimB, 1)
	claimsPerConsumer := records / max(consumers*max(granularityC, 1), 1)
	return min(ceiling, max(1, claimsPerConsumer))
}

// entryBatch is one element of the counting dispatch channel (plan 16): up to
// Beff records copied by value into a fixed [dispatchClaimBatch] array
// (8 + 24·16 = 392 B), so sending allocates nothing beyond the preallocated
// channel buffer. Beff ≤ dispatchClaimBatch always holds by effectiveBatch.
type entryBatch struct {
	n     int
	items [dispatchClaimBatch]cache.Entry
}

// countTasks is the reversal counting phase (specs/counter.md, plans 13+16+17):
// after the generation barrier the task-cache is sealed once — the same View
// serves the early-stop memo (Get) and the dispatch walk (All). A single
// producer copies records out of All into fixed-size batches on a bounded
// FIFO channel; consumer goroutines run the early-stop count-DFS for every
// delivered record off any lock, folding w · f(task) into the shared total.
// The mechanics — one buffered channel, close after the walk, consumers that
// drain until close — are workerpool.Fanout; its deadlock-free contract is
// exactly the phase's: the producer blocks only on sends and consumers never
// wait for anything but close. A terminated context ends the walk at the next
// shard boundary, drops the tail batch and closes the channel; consumers then
// skip every remaining task (ctx.Err() check in countOneTask) — partial
// total, no panic. capacityPerConsumer is the per-consumer record depth K:
// production passes dispatchCapacityPerConsumer, tests force backpressure
// with a smaller value.
func (c *Counter) countTasks(ctx context.Context, monitor monitoring.Monitor, workers int, taskCache *cache.Cache, precomputeDepth, capacityPerConsumer int) uint64 {
	monitor.BeginPhase("counting")
	// Seal is the barrier read: every gen-B writer finished before this call,
	// so the lock-free memo and the dispatch walk see all records (plan 16).
	view := taskCache.Seal()
	records := view.Len()
	monitor.AddTasks(records)

	consumers := max(min(workers, records), 1)
	batch := effectiveBatch(records, consumers, dispatchClaimBatch, dispatchGranularityC)
	// Small tables fit into the buffer entirely (the producer never blocks);
	// big ones keep the K-deep backpressure bound per consumer, expressed in
	// whole batch slots (specs/counter.md). ceil-division keeps ≥ 1 slot.
	capacity := max(min(consumers*capacityPerConsumer, records), 1)

	var total atomic.Uint64
	workerpool.Fanout(ctx, consumers, (capacity+batch-1)/batch,
		func(ctx context.Context, out chan<- entryBatch) {
			dispatchEntries(ctx, view, batch, out)
		},
		func(ctx context.Context, b entryBatch) {
			for _, e := range b.items[:b.n] {
				c.countOneTask(ctx, monitor, view, precomputeDepth, e.Path, e.Weight, &total)
			}
		})

	return total.Load()
}

// dispatchEntries streams the sealed table through ch in full batches of Beff
// records (the walk's tail, if any, is the final send). A terminated context
// ends the underlying All walk at the next shard boundary and aborts sends —
// the partial prefix already delivered stays dispatched, the tail is dropped
// (partial-run semantics, specs/counter.md). The calling contour closes ch.
func dispatchEntries(ctx context.Context, view *cache.View, batch int, ch chan<- entryBatch) {
	var b entryBatch
	for e := range view.All(ctx) {
		b.items[b.n] = e
		b.n++
		if b.n < batch {
			continue
		}
		if !sendBatch(ctx, ch, &b) {
			return
		}
		b.n = 0
	}
	if b.n > 0 && ctx.Err() == nil {
		sendBatch(ctx, ch, &b) // the walk's tail, deliverable like any full batch
	}
}

// sendBatch hands one batch to the consumers, reporting false when ctx ended
// before the slot was free: the batch is dropped and dispatch stops. The copy
// into the channel buffer happens here — the pointer argument only avoids one
// more 392 B hop at the call boundary. Blocking on a full buffer is legal:
// consumers never wait for the producer and always drain until close
// (specs/counter.md).
func sendBatch(ctx context.Context, ch chan<- entryBatch, b *entryBatch) bool {
	select {
	case ch <- *b:
		return true
	case <-ctx.Done():
		return false
	}
}

// countOneTask runs the early-stop count-DFS for a single task and folds its
// weighted path count and per-task statistics into the shared counters. It is
// invoked from a counting consumer, off every lock; a terminated context
// skips the task (partial-run semantics, specs/counter.md).
func (c *Counter) countOneTask(ctx context.Context, monitor monitoring.Monitor, memo *cache.View, precomputeDepth int, p path.Path, weight uint64, total *atomic.Uint64) {
	if ctx.Err() != nil {
		return
	}
	result := c.searcher.CountPathsWithCacheReversal(ctx, p, memo, precomputeDepth)
	paths := uint64(result.TotalPathsFound) * weight
	total.Add(paths)
	monitor.ReportPathsFound(int(paths))
	monitor.ReportSubtask(&result)
	monitor.ReportTaskCompleted()
}

// generateIntermediate runs phase A: DFS from every canonical start group to
// base = min(precomputeDepth, TwoPhaseBaseDepth), writing D4-canonical
// prefixes directly into an intermediate cache.Cache via Set (specs/counter.md,
// ADR-018). After the workerpool.Run barrier stops all writers, the table is
// materialized into phase B's independent-task worklist and dropped to the GC.
func (c *Counter) generateIntermediate(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) []cache.Entry {
	monitor.BeginPhase("gen A")
	intermediate := cache.NewCache()
	groups := c.symmetry.GetCanonicalGroups()
	monitor.AddTasks(len(groups))

	base := min(precomputeDepth, TwoPhaseBaseDepth)
	// The crew claims groups off one atomic cursor — the same ceiling as the
	// old per-group spawn under SetLimit(workers): min(workers, len(groups)).
	workerpool.New(workers).Run(ctx, len(groups), func(ctx context.Context, idx iter.Seq[int]) {
		for i := range idx {
			group := groups[i]
			result := c.searcher.GenerateTasks(ctx, intermediate, group.Canonical, uint64(group.OrbitSize), base)
			monitor.ReportSubtask(&result)
			monitor.ReportTaskCompleted()
		}
	})

	return materializeWorklist(ctx, intermediate)
}

// materializeWorklist seals the gen-A table (its writers stopped at the Run
// barrier) and flattens it into the phase-B worklist with a single
// All walk into a Len-sized slice — no intermediate copies beyond the slice
// itself (specs/counter.md). A terminated context leaves the partial worklist
// (partial-run semantics; the iterator has no other failure mode).
func materializeWorklist(ctx context.Context, intermediate *cache.Cache) []cache.Entry {
	view := intermediate.Seal()
	entries := make([]cache.Entry, 0, view.Len())
	for e := range view.All(ctx) {
		entries = append(entries, e)
	}
	return entries
}
