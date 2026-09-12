package counter

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/pruner"
	"knighttour/searcher"
	"knighttour/shapecount"
	"knighttour/state"
	"knighttour/symmetry"
	"knighttour/types"
)

// TwoPhaseBaseDepth is the intermediate accumulator depth of generation phase A.
// Phase A stays single-pass (parallel over canonical start groups — enough for
// its tiny trees); phase B extends every intermediate entry independently to
// the target depth, exposing thousands of tasks instead of ~10 groups. When
// precomputeDepth ≤ it, phase B degenerates: class weights are emitted from
// the intermediate entries themselves.
const TwoPhaseBaseDepth = 5

// defaultPrecomputeDepths is the per-board default split depth: measured
// optima on 5×5/6×6 sweeps, a sweep-tuned value on 7×7 and a conservative one
// on 8×8 (deeper splits grow the M accumulator exponentially and can exhaust
// memory). main.go applies it when -precompute-depth is not set.
var defaultPrecomputeDepths = map[int]int{5: 6, 6: 10, 7: 20, 8: 14}

// DefaultPrecomputeDepth returns the recommended precompute depth for a board
// size (the table above; falls back to TwoPhaseBaseDepth+1 for unknown sizes).
func DefaultPrecomputeDepth(size int) int {
	if d, ok := defaultPrecomputeDepths[size]; ok {
		return d
	}
	return TwoPhaseBaseDepth + 1
}

type Counter struct {
	graph       *graph.Graph
	symmetry    *symmetry.Symmetry
	searcher    *searcher.Searcher
	shapeDump   func(shape state.State, ends []int, allZero bool)
	tailK       int
	tailSlots   int
	shapeFilter pruner.L2Checks
}

// SetShapeFilter configures the final pass pre-DP shape feasibility filter
// (specs/shapecount.md, plan 02): pruner checks that kill provably-zero ends
// before the DP runs. Default shapecount.DefaultShapeFilter; L2None disables
// (bit-identical to the pre-filter pipeline). Call before counting.
func (c *Counter) SetShapeFilter(mask pruner.L2Checks) { c.shapeFilter = mask }

// SetShapeDump installs a diagnostic hook called from the final pass for every
// shape task with its queried ends and whether all h came out zero (plan 02
// stage-0 classification). fn is invoked concurrently from the counting
// workers and must be thread-safe; nil (the default) disables it. The ends
// slice aliases the worker's scratch buffer and is valid only for the call
// duration — sinks that keep it must copy. Intended for tooling/tests, not
// production runs.
func (c *Counter) SetShapeDump(fn func(shape state.State, ends []int, allZero bool)) {
	c.shapeDump = fn
}

// SetTailMemo enables the persistent per-worker tail memo of the final pass
// (specs/shapecount.md, plan 03 variant B): DP subproblems f(cur,todo) with
// popcount(todo) ≤ k are kept between shapes of one worker. k ≤ 0 disables
// it (the default). slots caps one worker's table (0 = default cap). Call
// before counting.
func (c *Counter) SetTailMemo(k, slots int) {
	c.tailK = max(k, 0)
	if slots > 0 {
		c.tailSlots = slots
	}
}

func NewCounter(g *graph.Graph) *Counter {
	size := g.Size()
	sym := symmetry.NewSymmetry(size)
	searcherObj := searcher.NewSearcher(g, sym)
	return &Counter{
		graph:       g,
		symmetry:    sym,
		searcher:    searcherObj,
		shapeFilter: shapecount.DefaultShapeFilter,
	}
}

// ParallelCount counts all open tours with the default split depth for the
// board size.
func (c *Counter) ParallelCount(ctx context.Context, monitor monitoring.Monitor, workers int) uint64 {
	return c.ParallelCountWithDepth(ctx, monitor, workers, DefaultPrecomputeDepth(c.graph.Size()))
}

// ParallelCountWithDepth counts all open tours via the single class-mode
// pipeline (specs/shapecount.md): generation leaves accumulate weights M by
// D4+translation shape class through per-worker LocalSinks, and a final pass
// computes h per shape once via DP — total = Σ h(C)·M(C). precomputeDepth is
// the meet-in-the-middle split (validated ≤ size²/2 by main.go); deeper than
// that duplicates the dual cut of the reversed tour.
func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers, precomputeDepth int) uint64 {
	intermediate := c.generateIntermediate(ctx, monitor, workers, precomputeDepth)

	monitor.BeginPhase("gen B")
	monitor.AddTasks(len(intermediate))

	acc := cache.NewAccumulator()
	nextEntry := atomic.Int64{}
	// Chunk workers (not g.Go per entry): each owns one LocalSink across many
	// tasks, so hot classes collapse locally before touching shard locks.
	g, gctx := errgroup.WithContext(ctx)
	for range min(len(intermediate), workers) {
		g.Go(func() error {
			sink := acc.Local()
			defer sink.Flush()
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
				result := c.searcher.ExtendToClasses(gctx, sink, e.Path, e.Weight, precomputeDepth)
				monitor.ReportSubtask(&result)
				monitor.ReportTaskCompleted()
			}
		})
	}
	_ = g.Wait()

	return c.countShapes(ctx, monitor, workers, acc)
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

// shapeTask groups all accumulator ends belonging to one normalized shape so
// a single DP pass with a shared memo answers every h of that shape. Ends of
// one shape form the range [lo, hi) of the shard slice sorted by State (all
// ends of a class share a shard — cache.md sharding invariant), so grouping
// needs no intermediate copies and no global sort.
type shapeTask struct {
	lo, hi int
}

func (t shapeTask) len() int { return t.hi - t.lo }

// groupSorted sorts one drained shard by State and cuts it into per-shape
// ranges; tasks are ordered by descending end count (DP cost proxy).
func groupSorted(entries []cache.Entry) []shapeTask {
	slices.SortFunc(entries, func(a, b cache.Entry) int {
		return cmp.Compare(a.Path.State(), b.Path.State())
	})
	var tasks []shapeTask
	for lo := 0; lo < len(entries); {
		hi := lo + 1
		for hi < len(entries) && entries[hi].Path.State() == entries[lo].Path.State() {
			hi++
		}
		tasks = append(tasks, shapeTask{lo: lo, hi: hi})
		lo = hi
	}
	// Expensive shapes first (more ends ≈ more DP work): better tail balance.
	slices.SortFunc(tasks, func(a, b shapeTask) int { return cmp.Compare(b.len(), a.len()) })
	return tasks
}

// claimBatch is the granularity of task claiming: one cursor bump per batch
// keeps atomics an order of magnitude below the task count.
const claimBatch = 16

// shardJob is one grouped shard. Tasks are claimed in batches via the atomic
// cursor; the entries slice outlives the job until every task of it has been
// claimed (claimers only read).
type shardJob struct {
	entries []cache.Entry
	tasks   []shapeTask
	next    atomic.Int64
}

// claim takes the next batch of tasks, reporting whether any was left.
func (j *shardJob) claim() (lo int, ok bool) {
	i := j.next.Add(claimBatch) - claimBatch
	if i >= int64(len(j.tasks)) {
		return 0, false
	}
	return int(i), true
}

// groupShards is the first stage of the final pass: it drains M shard by shard
// and cuts every shard into per-shape jobs. Jobs come back ordered by ascending
// end count, because the second stage pops its stack from the tail — so the
// biggest shards are claimed first (LPT) and no worker idles behind one hot
// shard. No job is created while tasks run, hence no sleeping protocol there.
func groupShards(ctx context.Context, monitor monitoring.Monitor, workers int, acc *cache.Accumulator) []*shardJob {
	byShard := make([]*shardJob, acc.NumShards())
	var nextShard atomic.Int64

	g, gctx := errgroup.WithContext(ctx)
	for range min(workers, len(byShard)) {
		g.Go(func() error {
			for {
				select {
				case <-gctx.Done():
					return nil
				default:
				}
				i := int(nextShard.Add(1)) - 1
				if i >= len(byShard) {
					return nil
				}
				entries := acc.DrainShard(i)
				if len(entries) == 0 {
					continue
				}
				tasks := groupSorted(entries)
				monitor.AddTasks(len(tasks))
				byShard[i] = &shardJob{entries: entries, tasks: tasks}
			}
		})
	}
	_ = g.Wait()

	jobs := make([]*shardJob, 0, len(byShard))
	for _, j := range byShard {
		if j != nil {
			jobs = append(jobs, j)
		}
	}
	slices.SortFunc(jobs, func(a, b *shardJob) int {
		return cmp.Compare(len(a.entries), len(b.entries))
	})
	return jobs
}

// processShapes is the second stage of the final pass: workers claim batches
// from the shared stack of grouped shards (biggest last, i.e. claimed first).
// An empty stack means everything was claimed — workers exit without waiting,
// because the stack never grows during this stage.
func processShapes(ctx context.Context, monitor monitoring.Monitor, sc *shapecount.Counter, workers int, jobs []*shardJob, dump func(shape state.State, ends []int, allZero bool)) (totalPaths uint64, zeroShapes int64) {
	var (
		mu    sync.Mutex
		stack = jobs
		paths atomic.Uint64
		zeros atomic.Int64
	)

	// nextJob claims a batch from the biggest not-yet-exhausted shard.
	nextJob := func(gctx context.Context) (*shardJob, int) {
		mu.Lock()
		defer mu.Unlock()
		for len(stack) > 0 {
			if gctx.Err() != nil {
				return nil, 0
			}
			j := stack[len(stack)-1]
			if lo, ok := j.claim(); ok {
				return j, lo
			}
			stack = stack[:len(stack)-1]
		}
		return nil, 0
	}

	g, gctx := errgroup.WithContext(ctx)
	for range min(workers, len(jobs)) {
		g.Go(func() error {
			// One tail per worker: small subproblems persist across the shapes
			// this goroutine counts (plan 03 variant B); nil when disabled.
			tail := sc.NewTail()
			var ends []int
			for {
				j, lo := nextJob(gctx)
				if j == nil {
					return nil
				}
				for _, t := range j.tasks[lo:min(lo+claimBatch, len(j.tasks))] {
					e := j.entries[t.lo:t.hi]
					if cap(ends) < t.len() {
						ends = make([]int, 0, t.len())
					} else {
						ends = ends[:0]
					}
					for _, ce := range e {
						ends = append(ends, ce.Path.End())
					}
					var stats types.Result
					hs := sc.CountShapeWithTail(e[0].Path.State(), ends, &stats, tail)

					var contribution uint64
					allZero := true
					for k, h := range hs {
						if h == 0 {
							continue
						}
						allZero = false
						contribution += h * e[k].Weight
					}
					if allZero {
						zeros.Add(1)
					}
					if dump != nil {
						dump(e[0].Path.State(), ends, allZero)
					}
					paths.Add(contribution)
					monitor.ReportPathsFound(int(contribution))
					monitor.ReportSubtask(&stats)
					monitor.ReportTaskCompleted()
				}
			}
		})
	}
	_ = g.Wait()

	return paths.Load(), zeros.Load()
}

// countShapes runs the final pass: every shape of M is evaluated exactly once
// via shapecount DP, summing Σ h(C)·M(C). Grouping happens first (groupShards),
// execution second (processShapes); no global snapshot of M is ever built — a
// shard's map is released as soon as its flat slice is taken, specs/counter.md.
func (c *Counter) countShapes(ctx context.Context, monitor monitoring.Monitor, workers int, acc *cache.Accumulator) uint64 {
	monitor.BeginPhase("counting")

	jobs := groupShards(ctx, monitor, workers, acc)
	var classes, shapes int64
	for _, j := range jobs {
		classes += int64(len(j.entries))
		shapes += int64(len(j.tasks))
	}

	sc := shapecount.New(c.graph)
	sc.SetTailMemo(c.tailK, c.tailSlots)
	sc.SetShapeFilter(c.shapeFilter)
	totalPaths, zeroShapes := processShapes(ctx, monitor, sc, workers, jobs, c.shapeDump)

	monitor.ReportShapeStats(int(classes), int(shapes), int(zeroShapes))
	return totalPaths
}
