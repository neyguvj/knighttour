package counter

import (
	"context"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/path"
	"knighttour/searcher"
	"knighttour/symmetry"
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

// dispatchStackCapacity is the per-consumer depth K of the counting-phase
// shared stack (plan 13): the stack preallocates consumers×K cache.Entry
// slots — 24 B × K × max(min(workers, records), 1) for the phase, zero
// allocations per record. Fixed at the measured best point of the plan-13
// sweep on 7×7 d22 (window [1000..10000], ADR-019); there is no runtime
// override (ADR-020).
const dispatchStackCapacity = 10000

// dispatchClaimBatch is the flush/claim batch ceiling B of plan 13: the
// effective granularity comes from effectiveBatch (per-record on small
// tables, saturating to B), one synchronization per Beff records on each
// side. Fixed at the measured optimum (ADR-019; ADR-010's claim batch was
// 16 too).
const dispatchClaimBatch = 16

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
// (specs/counter.md, ADR-016): gen A over canonical start groups into an
// intermediate cache.Cache, gen B chunk workers extending each intermediate
// entry into the live task-cache (both phases write direct Set, ADR-018), and
// counting dispatching every task-cache record through a shared bounded LIFO
// stack to the early-stop count-DFS — total =
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

	return c.countTasks(ctx, monitor, workers, taskCache, precomputeDepth, dispatchStackCapacity)
}

// effectiveBatch computes the phase's single flush/claim granularity Beff once
// at phase start from ItemsCount(): min(B, max(1, records/(consumers·C))) —
// per-record delivery while records ≤ consumers·C (small tables never pile up
// on one consumer), saturating to the ceiling B once records ≥ B·consumers·C
// (specs/counter.md). Both producer flushes and consumer claims use it. The
// knobs are explicit parameters: production passes the fixed defaults, tests
// pin the formula at other knobs without mutating package state.
func effectiveBatch(records, consumers, claimB, granularityC int) int {
	ceiling := max(claimB, 1)
	claimsPerConsumer := records / max(consumers*max(granularityC, 1), 1)
	return min(ceiling, max(1, claimsPerConsumer))
}

// countTasks is the reversal counting phase (specs/counter.md, plan 13): the
// Cache.Each walk of the task-cache becomes a producer — its callback copies
// each record (cache.Entry, 24 B) into one shared bounded LIFO stack instead
// of counting under the shard read lock — and consumer goroutines pop batches
// off the stack top and run the early-stop count-DFS off every shard lock,
// folding w · f(task) into the shared total. The copy is legal because no
// writer exists after the generation barrier; the memo Get still reads the
// live cache (specs/cache.md). A producer finding the stack full parks until
// consumers free space — deadlock-free because consumers never wait for
// anything but closed-and-empty and always drain. A terminated context stops
// the producer (Each returns ctx.Err()); the stack then closes and the
// consumers drain it without counting — partial total, no panic. stackK is
// the per-consumer stack depth: production passes dispatchStackCapacity,
// tests force overflow with a smaller value.
func (c *Counter) countTasks(ctx context.Context, monitor monitoring.Monitor, workers int, taskCache *cache.Cache, precomputeDepth, stackK int) uint64 {
	monitor.BeginPhase("counting")
	records := taskCache.ItemsCount()
	monitor.AddTasks(records)

	consumers := max(min(workers, records), 1)
	batch := effectiveBatch(records, consumers, dispatchClaimBatch, dispatchGranularityC)
	// Small tables fit into the stack entirely (producers never block); big
	// ones keep the K-deep backpressure bound per consumer (specs/counter.md).
	capacity := max(min(consumers*stackK, records), 1)
	stack := newTaskStack(capacity, batch)

	var total atomic.Uint64
	var wg sync.WaitGroup
	wg.Add(consumers)
	for range consumers {
		go func() {
			defer wg.Done()
			c.countStackTasks(ctx, monitor, taskCache, precomputeDepth, stack, &total)
		}()
	}

	// One walk goroutine (Each workers = 1): the callback has no per-goroutine
	// hook, so the private flush buffer below requires a single producer — and
	// its copy rate stays an order of magnitude above counting anyway.
	local := make([]cache.Entry, 0, batch)
	err := taskCache.Each(ctx, 1, func(_ context.Context, p path.Path, weight uint64) error {
		local = append(local, cache.Entry{Path: p, Weight: weight})
		if len(local) == batch {
			stack.push(local)
			local = local[:0]
		}
		return nil
	})
	if len(local) > 0 && ctx.Err() == nil {
		stack.push(local) // the walk's tail, flushable like any full batch
	}

	stack.close() // Each returned — every producer is done.
	wg.Wait()
	checkEachError(ctx, err)

	return total.Load()
}

// countStackTasks is one counting consumer: it pops batches from the shared
// stack until it closes empty, running the early-stop count-DFS for every
// record off every shard lock. The claim buffer belongs to this goroutine —
// pop copies into it and frees the slots immediately.
func (c *Counter) countStackTasks(ctx context.Context, monitor monitoring.Monitor, taskCache *cache.Cache, precomputeDepth int, stack *taskStack, total *atomic.Uint64) {
	buf := make([]cache.Entry, 0, stack.batch)
	for {
		var ok bool
		buf, ok = stack.pop(buf[:0])
		if !ok {
			return
		}
		for _, e := range buf {
			c.countOneTask(ctx, monitor, taskCache, precomputeDepth, e.Path, e.Weight, total)
		}
	}
}

// taskStack is the shared bounded LIFO stack of record copies between the
// Cache.Each producer(s) and the counting consumers (plan 13, the class-mode
// stack of ADR-010 without drain and without LPT): producers push flush
// batches, consumers claim up to batch entries per mutex grab from the top —
// newest first, the single claim order of the contract (ADR-020). The
// capacity is fixed at construction and never grows; a producer on a full
// stack parks until a consumer frees slots. Popped entries are copied into
// the claimer's scratch buffer, so slots are reusable at once. closed marks
// the end of production; consumers exit on closed-and-empty — no entry ever
// appears after that, so it is the phase's end.
type taskStack struct {
	notEmpty *sync.Cond
	notFull  *sync.Cond
	slots    []cache.Entry
	count    int // live entries; the newest sits at slots[count-1]
	batch    int
	mu       sync.Mutex
	closed   bool
}

// newTaskStack returns a stack of the given capacity (≥ 1, specs/counter.md)
// and consumer claim size.
func newTaskStack(capacity, batch int) *taskStack {
	s := &taskStack{slots: make([]cache.Entry, capacity), batch: batch}
	s.notEmpty = sync.NewCond(&s.mu)
	s.notFull = sync.NewCond(&s.mu)
	return s
}

// push appends a whole flush batch, parking while the stack is full and
// copying as much as fits per grab (a batch may exceed the capacity). The
// producer barrier in countTasks keeps it from racing close.
func (s *taskStack) push(batch []cache.Entry) {
	for len(batch) > 0 {
		s.mu.Lock()
		for s.count == len(s.slots) {
			s.notFull.Wait()
		}
		n := copy(s.slots[s.count:], batch)
		s.count += n
		s.mu.Unlock()
		// Broadcast, not Signal: one flush can feed several consumers parked
		// on an empty stack, and a single wake-up would leave the others
		// sleeping with entries still available.
		s.notEmpty.Broadcast()
		batch = batch[n:]
	}
}

// pop claims the newest min(batch, count) entries — the LIFO top — copying
// them into dst (capacity ≥ batch), so the slots are reusable immediately.
// ok is false once the stack is closed and drained: the consumer's exit
// condition; parking on empty waits for a push or for close.
func (s *taskStack) pop(dst []cache.Entry) ([]cache.Entry, bool) {
	s.mu.Lock()
	for s.count == 0 && !s.closed {
		s.notEmpty.Wait()
	}
	if s.count == 0 {
		s.mu.Unlock()
		return dst[:0], false
	}
	lo := s.count - min(s.batch, s.count)
	dst = append(dst, s.slots[lo:s.count]...)
	s.count = lo
	s.mu.Unlock()
	s.notFull.Signal() // the producer may park on exactly this freed space
	return dst, true
}

// close marks the end of production and wakes every parked consumer so each
// sees closed-and-empty and exits. Called strictly after every producer
// returned, so no notFull waiter exists at that point.
func (s *taskStack) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.notEmpty.Broadcast()
}

// countOneTask runs the early-stop count-DFS for a single task and folds its
// weighted path count and per-task statistics into the shared counters. It is
// invoked from a counting consumer, off every shard lock; a terminated context
// skips the task (partial-run semantics, specs/counter.md).
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
// base = min(precomputeDepth, TwoPhaseBaseDepth), writing D4-canonical
// prefixes directly into an intermediate cache.Cache via Set (specs/counter.md,
// ADR-018). After the errgroup barrier stops all writers, the table is
// materialized into phase B's independent-task worklist and dropped to the GC.
func (c *Counter) generateIntermediate(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) []cache.Entry {
	monitor.BeginPhase("gen A")
	intermediate := cache.NewCache()
	groups := c.symmetry.GetCanonicalGroups()
	monitor.AddTasks(len(groups))

	base := min(precomputeDepth, TwoPhaseBaseDepth)
	// gctx, not ctx: errgroup cancels its derived context on every Wait, so
	// the post-barrier Each walk must run on the parent.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for _, group := range groups {
		p := group.Canonical
		g.Go(func() error {
			result := c.searcher.GenerateTasks(gctx, intermediate, p, uint64(group.OrbitSize), base)
			monitor.ReportSubtask(&result)
			monitor.ReportTaskCompleted()
			return nil
		})
	}
	_ = g.Wait()

	return materializeWorklist(ctx, intermediate)
}

// materializeWorklist flattens the gen-A table into the phase-B worklist with
// a single Each goroutine — no mutex on the shared slice. A terminated context
// leaves the partial worklist (partial-run semantics, specs/counter.md); any
// other Each error fails loudly via checkEachError.
func materializeWorklist(ctx context.Context, intermediate *cache.Cache) []cache.Entry {
	entries := make([]cache.Entry, 0, intermediate.ItemsCount())
	err := intermediate.Each(ctx, 1, func(_ context.Context, p path.Path, weight uint64) error {
		entries = append(entries, cache.Entry{Path: p, Weight: weight})
		return nil
	})
	checkEachError(ctx, err)
	return entries
}

// checkEachError enforces the pipeline's cancellation semantics
// (specs/counter.md): a walk ended by a terminated context — cancelled or
// expired — keeps its partial result, while any other Cache.Each error is
// unreachable per contract (the callback never fails, specs/cache.md) and
// means a bug in Each itself, so it fails loudly instead of counting on
// silently truncated data.
func checkEachError(ctx context.Context, err error) {
	if err != nil && ctx.Err() == nil {
		panic("counter: unexpected Cache.Each error: " + err.Error())
	}
}
