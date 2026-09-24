package counter

import (
	"context"
	"runtime/metrics"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/path"
)

func TestSequentalCount(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 1)

	assert.Equal(t, uint64(1728), count, "Expected %d count for 5x5 board, got %d", 1728, count)
}

func TestParallelCount(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 8)

	assert.Equal(t, uint64(1728), count, "Expected %d count for 5x5 board, got %d", 1728, count)
}

func TestParallelCountWithDepth(t *testing.T) {
	size := 5
	g := graph.New(size)
	counter := NewCounter(g)

	for depth := 1; depth <= size*size/2; depth++ { // full meet-in-the-middle range
		count := counter.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), 8, depth)
		assert.Equal(t, uint64(1728), count, "Expected %d count for 5x5 board at depth %d", 1728, depth)
	}
}

// The counting phase publishes task-cache hits/misses into its own counters
// (specs/counter.md): the reversal stop level must answer from the cache.
func TestCountingPublishesHitsMisses(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	fm := monitoring.NewFakeMonitor()
	assert.Equal(t, uint64(1728), counter.ParallelCountWithDepth(context.Background(), fm, 8, 6))

	counting := fm.Phase("counting")
	assert.Positive(t, counting.CacheHits, "the stop level must hit the task cache")
}

// The count-DFS prunes while descending to the stop level and reports those
// cuts through ReportSubtask into the counting phase (specs/counter.md).
func TestCountingPhaseReportsPruning(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	fm := monitoring.NewFakeMonitor()
	assert.Equal(t, uint64(1728), counter.ParallelCountWithDepth(context.Background(), fm, 8, 6))

	counting := fm.Phase("counting")
	assert.Positive(t, counting.Pruned, "count-DFS pruning must be reported")
	assert.Equal(t,
		counting.PrunedDeadEnd+counting.PrunedNoCont+counting.PrunedDisconn+counting.PrunedEndpoints,
		counting.Pruned, "pruned total equals the per-reason breakdown")
}

// The forced-chain write gate runs on the leaves of both generation phases and
// its reason reaches the phase counters (specs/counter.md, ADR-030); the
// counting phase never runs the gate, so its gate counter stays zero.
func TestGenerationPhasesReportWriteGate(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	fm := monitoring.NewFakeMonitor()
	assert.Equal(t, uint64(1728), counter.ParallelCountWithDepth(context.Background(), fm, 8, 6))

	genA := fm.Phase("gen A")
	assert.Positive(t, genA.PrunedForcedChain, "gen A leaves must pass the gate")
	genB := fm.Phase("gen B")
	assert.Positive(t, genB.PrunedForcedChain, "gen B leaves must pass the gate")

	counting := fm.Phase("counting")
	assert.Zero(t, counting.PrunedForcedChain, "the write gate belongs to generation only")
}

func TestParallelCountWithDepthMatchesReference(t *testing.T) {
	tests := []struct {
		name     string
		depths   []int
		size     int
		expected uint64
	}{
		{name: "5x5 depths 1-12", size: 5, expected: 1728, depths: []int{1, 2, 3, 4, 6, 9, 12}},
		// 6×6 sweeps are slow; sample shallow + the default-ish middle.
		{name: "6x6 sampled depths", size: 6, expected: 6_637_920, depths: []int{1, 5, 8, 14}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New(tt.size)
			counter := NewCounter(g)

			for _, depth := range tt.depths {
				count := counter.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), 4, depth)
				assert.Equal(t, tt.expected, count, "size=%d depth=%d", tt.size, depth)
			}
		})
	}
}

// Results must be deterministic in the worker count (work-stealing order).
func TestWorkerInvariance(t *testing.T) {
	const size = 5
	g := graph.New(size)
	counter := NewCounter(g)

	for _, depth := range []int{3, 6, 12} {
		countSeq := counter.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), 1, depth)
		assert.Equal(t, uint64(1728), countSeq, "depth=%d", depth)
		for _, workers := range []int{3, 8} {
			count := counter.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), workers, depth)
			assert.Equal(t, countSeq, count, "depth=%d: result must not depend on worker count", depth)
		}
	}
}

// A terminated context — cancelled or expired — must end the pipeline as a
// partial run without a panic (specs/counter.md): both Cache.Each walks treat
// any ctx termination alike (counting stops producing and its consumers drain
// the closed stack without counting), only non-ctx errors are bugs.
func TestTerminatedContextPartialRun(t *testing.T) {
	tests := []struct {
		ctxFunc func() context.Context
		name    string
	}{
		{name: "cancelled", ctxFunc: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			return ctx
		}},
		{name: "deadline exceeded", ctxFunc: func() context.Context {
			ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
			defer cancel()
			return ctx
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counter := NewCounter(graph.New(5))

			var count uint64
			assert.NotPanics(t, func() {
				count = counter.ParallelCountWithDepth(tt.ctxFunc(), monitoring.NewFakeMonitor(), 4, 6)
			}, "a terminated context must end the pipeline without a panic")
			assert.Zero(t, count, "nothing is generated or counted under an already terminated context")
		})
	}
}

// tableWeight sums every record's weight via a single-worker Each walk,
// asserting positivity along the way (specs/cache.md: zeros are never stored).
func tableWeight(t *testing.T, c *cache.Cache) uint64 {
	t.Helper()
	total := uint64(0)
	err := c.Each(context.Background(), 1, func(_ context.Context, _ path.Path, weight uint64) error {
		assert.Positive(t, weight, "weight must be positive")
		total += weight
		return nil
	})
	require.NoError(t, err)
	return total
}

func TestGenerateIntermediateWeightsMatchOrbits(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)
	ctx := context.Background()

	const base = 2

	// Per group: every raw prefix contributes its orbit size to the table,
	// so total weight must equal CacheWrites * OrbitSize regardless of merging.
	expectedWeight := 0
	for _, group := range counter.symmetry.GetCanonicalGroups() {
		if g.SholdSkip(group.Canonical) {
			continue
		}

		groupCache := cache.NewCache()
		result := counter.searcher.GenerateTasks(ctx, groupCache, group.Canonical, uint64(group.OrbitSize), base)
		totalWeight := tableWeight(t, groupCache)

		assert.Equal(t, uint64(result.CacheWrites)*uint64(group.OrbitSize), totalWeight,
			"group %d: total weight equals prefixes * orbit size", group.Canonical)
		expectedWeight += int(totalWeight)
	}

	// Merging across groups conserves the total weight.
	entries := counter.generateIntermediate(ctx, monitoring.NewFakeMonitor(), 4, base)
	fullWeight := uint64(0)
	for _, e := range entries {
		fullWeight += e.Weight
	}

	assert.Equal(t, uint64(expectedWeight), fullWeight, "cross-group merging conserves total weight")
}

// Phase A must stop at min(depth, TwoPhaseBaseDepth): with a deeper target
// every intermediate entry sits exactly at the base depth.
func TestGenerateIntermediateStopsAtBaseDepth(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	entries := counter.generateIntermediate(context.Background(), monitoring.NewFakeMonitor(), 8, 7)
	assert.NotEmpty(t, entries)
	for _, e := range entries {
		assert.Equal(t, TwoPhaseBaseDepth, e.Path.State().CountBits())
	}
}

func TestDefaultPrecomputeDepth(t *testing.T) {
	assert.Equal(t, 6, DefaultPrecomputeDepth(5))
	assert.Equal(t, 14, DefaultPrecomputeDepth(6))
	assert.Equal(t, 22, DefaultPrecomputeDepth(7))
	assert.Equal(t, 14, DefaultPrecomputeDepth(8))
	assert.Equal(t, TwoPhaseBaseDepth+1, DefaultPrecomputeDepth(9))
}

// gcPercentNow reports the GC percent currently in effect in this process
// (the read-only view of debug.SetGCPercent / GOGC).
func gcPercentNow() int {
	s := []metrics.Sample{{Name: "/gc/gogc:percent"}}
	metrics.Read(s)
	return int(int64(s[0].Value.Uint64()))
}

// gcSpyMonitor records the effective GC percent at every BeginPhase so tests
// can observe what a running pipeline applied (BeginPhase is called strictly
// between phases, never concurrently with itself).
type gcSpyMonitor struct {
	*monitoring.FakeMonitor
	phasePercents []int
}

// BeginPhase samples the runtime GC percent, then delegates to the fake.
func (m *gcSpyMonitor) BeginPhase(name string) {
	m.phasePercents = append(m.phasePercents, gcPercentNow())
	m.FakeMonitor.BeginPhase(name)
}

// ADR-014: the pipeline runs entirely under the configured GC percent and
// restores the previous one on exit; the total is invariant to the knob.
func TestGCPercentAppliesAndRestores(t *testing.T) {
	tests := []struct {
		name      string
		gcPercent int
	}{
		{name: "knob at default 40", gcPercent: DefaultGCPercentReversal},
		{name: "knob off", gcPercent: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := gcPercentNow()
			wantDuring := before // p == 0 must leave the runtime percent alone
			if tt.gcPercent > 0 {
				wantDuring = tt.gcPercent
			}

			counter := NewCounter(graph.New(5))
			counter.SetGCPercent(tt.gcPercent)

			spy := &gcSpyMonitor{FakeMonitor: monitoring.NewFakeMonitor()}
			count := counter.ParallelCountWithDepth(context.Background(), spy, 4, 6)

			assert.Equal(t, uint64(1728), count, "total must not depend on the GC knob")
			if assert.NotEmpty(t, spy.phasePercents, "pipeline must report phases") {
				for _, got := range spy.phasePercents {
					assert.Equal(t, wantDuring, got, "every phase runs under the configured percent")
				}
			}
			assert.Equal(t, before, gcPercentNow(), "runtime percent must be restored after the pipeline")
		})
	}
}

// orDefault maps a zero table column to the production knob value, so rows
// only spell out the knob they override.
func orDefault(value, def int) int {
	if value == 0 {
		return def
	}
	return value
}

// The effective flush/claim granularity Beff of plan 13 is pinned at its formula
// boundaries (specs/counter.md): per-record while records ≤ consumers·C, ceiling B
// once records ≥ B·consumers·C. The rows are the measured 6×6 regression shapes and
// the saturation edge at workers = 14 (integer division); the knobs are explicit
// arguments (production defaults when the column is zero), never package state.
func TestEffectiveBatch(t *testing.T) {
	tests := []struct {
		name    string
		records int
		workers int
		claimB  int // 0 keeps the production ceiling B
		factorC int // 0 keeps the production constant C
		want    int
	}{
		{name: "empty table", records: 0, workers: 8, want: 1},
		{name: "6x6 d1 regression row", records: 6, workers: 14, want: 1},
		{name: "6x6 d2 regression row", records: 20, workers: 14, want: 1},
		{name: "6x6 d3 regression row", records: 73, workers: 14, want: 1},
		{name: "per-record at the consumers·C boundary", records: 56, workers: 14, want: 1},
		{name: "transition zone d4 row", records: 228, workers: 14, want: 4},
		{name: "transition zone d5 row", records: 653, workers: 14, want: 11},
		{name: "ceiling at B·consumers·C", records: 896, workers: 14, want: 16},
		{name: "ceiling above saturation", records: 131_800_000, workers: 14, want: 16},
		{name: "lone consumer is not throttled", records: 73, workers: 1, want: 16},
		{name: "ceiling knob", records: 896, workers: 14, claimB: 8, want: 8},
		{name: "C=1 shrinks the per-record zone", records: 73, workers: 14, factorC: 1, want: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consumers := max(min(tt.workers, tt.records), 1)
			claimB := orDefault(tt.claimB, dispatchClaimBatch)
			factorC := orDefault(tt.factorC, dispatchGranularityC)
			assert.Equal(t, tt.want, effectiveBatch(tt.records, consumers, claimB, factorC))
		})
	}
}

// A table below the batch ceiling — the plan-13 regression shapes — is delivered one
// record per claim (specs/counter.md): a stack wired with effectiveBatch hands out
// single entries while the formula is active and full B-sized claims once saturated.
func TestTaskStackClaimsFollowEffectiveBatch(t *testing.T) {
	tests := []struct {
		name      string
		records   int
		wantClaim int
	}{
		{name: "6x6 d1 shape, per record", records: 6, wantClaim: 1},
		{name: "6x6 d2 shape, per record", records: 20, wantClaim: 1},
		{name: "6x6 d3 shape, per record", records: 73, wantClaim: 1},
		{name: "saturated table, full batch", records: 896, wantClaim: dispatchClaimBatch},
	}

	const workers = 14
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consumers := max(min(workers, tt.records), 1)
			batch := effectiveBatch(tt.records, consumers, dispatchClaimBatch, dispatchGranularityC)
			require.Equal(t, tt.wantClaim, batch, "the formula must produce the claimed granularity")

			stack := newTaskStack(max(min(consumers*dispatchStackCapacity, tt.records), 1), batch)
			flush := make([]cache.Entry, batch)
			for range tt.records / batch {
				stack.push(flush)
			}
			if rest := tt.records % batch; rest > 0 {
				stack.push(flush[:rest])
			}
			stack.close()

			scratch := make([]cache.Entry, 0, batch)
			delivered := 0
			for {
				var ok bool
				scratch, ok = stack.pop(scratch[:0])
				if !ok {
					break
				}
				assert.LessOrEqual(t, len(scratch), batch, "a claim never exceeds Beff")
				if tt.wantClaim == 1 {
					assert.Len(t, scratch, 1, "the active formula delivers per record")
				}
				delivered += len(scratch)
			}
			assert.Equal(t, tt.records, delivered, "every record delivered exactly once")
		})
	}
}

// taskCacheAtDepth materializes the gen-A table of one depth as a standalone
// task-cache: its keys sit exactly at the counting stop level, so every memo
// lookup of countTasks can hit (with a mismatched table f(task) is zero).
func taskCacheAtDepth(c *Counter, ctx context.Context, depth int) *cache.Cache {
	taskCache := cache.NewCache()
	for _, e := range c.generateIntermediate(ctx, monitoring.NewFakeMonitor(), 4, depth) {
		taskCache.Set(e.Path, e.Weight)
	}
	return taskCache
}

// sequentialTotal computes the Σ w·f(task) reference of a whole table with a
// single-worker walk — the baseline the stack dispatch must reproduce. The
// 5x5 total is asserted too, or every dispatch equality against it is vacuous.
func sequentialTotal(t *testing.T, c *Counter, ctx context.Context, taskCache *cache.Cache, depth int) uint64 {
	t.Helper()
	want := uint64(0)
	require.NoError(t, taskCache.Each(ctx, 1, func(_ context.Context, p path.Path, w uint64) error {
		res := c.searcher.CountPathsWithCacheReversal(ctx, p, taskCache, depth)
		want += uint64(res.TotalPathsFound) * w
		return nil
	}))
	assert.Equal(t, uint64(1728), want, "the sequential reference must count the whole 5x5 tour total")
	return want
}

// The counting stack dispatch delivers every task-cache record to the
// consumers exactly once (specs/counter.md): the parallel total equals the
// sequential Σ w·f(task) reference and the phase completes exactly ItemsCount
// tasks, whatever the worker count — including a below-ceiling table with the
// Beff formula active. LIFO is the only claim order (ADR-020).
func TestCountTasksDeliversEachEntryOnce(t *testing.T) {
	tests := []struct {
		name  string
		depth int
	}{
		{name: "table above the batch ceiling", depth: 4},
		{name: "table below the batch ceiling, formula active", depth: 1},
	}

	c := NewCounter(graph.New(5))
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskCache := taskCacheAtDepth(c, ctx, tt.depth)
			items := taskCache.ItemsCount()
			require.Positive(t, items)
			if tt.depth == 1 {
				require.Less(t, items, dispatchClaimBatch, "the table must stay below the batch ceiling")
			}

			want := sequentialTotal(t, c, ctx, taskCache, tt.depth)

			for _, workers := range []int{1, 4, 8} {
				fm := monitoring.NewFakeMonitor()
				got := c.countTasks(ctx, fm, workers, taskCache, tt.depth, dispatchStackCapacity)
				assert.Equal(t, want, got, "workers=%d: dispatch must not drop or duplicate records", workers)
				counting := fm.Phase("counting")
				assert.Equal(t, uint64(items), counting.Completed, "workers=%d: every record counted exactly once", workers)
				assert.Equal(t, uint64(items), counting.Tasks, "workers=%d: AddTasks saw the whole table", workers)
			}
		})
	}
}

// A stack far smaller than the record count makes every full-stack flush block
// under the shard RLock until the consumers drain; that must neither deadlock
// nor change the total (specs/counter.md), and stackK is the explicit seam of
// countTasks. The gated 5x5 depth-4 table (forced-chain write gate pruned its
// dead keys, ADR-030) holds a couple dozen records: with workers = 8 the Beff
// formula gives per-record delivery and K = 1 caps the stack at consumers×K
// slots — below the record count, so flushes block; with a lone consumer the
// effective batch is a handful of records and K lands the capacity exactly on
// one batch, so every flush waits for a drain.
func TestCountTasksForcedOverflow(t *testing.T) {
	tests := []struct {
		name    string
		workers int
		k       int // 0 = derive: lone-consumer capacity of exactly one batch
	}{
		{name: "K=1, stack of consumers slots", workers: 8, k: 1},
		{name: "capacity of exactly one batch", workers: 1, k: 0},
	}

	const depth = 4
	c := NewCounter(graph.New(5))
	ctx := context.Background()
	taskCache := taskCacheAtDepth(c, ctx, depth)
	items := taskCache.ItemsCount()
	require.Greater(t, items, 8, "the overflow scenario needs more records than the K=1 stack slots")
	want := sequentialTotal(t, c, ctx, taskCache, depth)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consumers := max(min(tt.workers, items), 1)
			batch := effectiveBatch(items, consumers, dispatchClaimBatch, dispatchGranularityC)
			fm := monitoring.NewFakeMonitor()
			got := c.countTasks(ctx, fm, tt.workers, taskCache, depth, orDefault(tt.k, batch))
			assert.Equal(t, want, got, "workers=%d: forced overflow must neither deadlock nor change the total", tt.workers)
		})
	}
}

// The stack is the phase's only hand-off (specs/counter.md): concurrent
// producers must deliver every record exactly once to a single consumer at any
// capacity — capacity 1 forces every flush through a full-stack block, and a
// batch wider than the capacity splits it across grabs.
func TestTaskStackExactOnce(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
	}{
		{name: "capacity 1, every flush blocks", capacity: 1},
		{name: "capacity 2", capacity: 2},
		{name: "capacity 5", capacity: 5},
	}

	const producers, perProducer = 4, 256
	total := producers * perProducer
	want := make([]uint64, total)
	for i := range want {
		want[i] = uint64(i)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := newTaskStack(tt.capacity, 16)
			var lane atomic.Uint64
			var wg sync.WaitGroup
			wg.Add(producers)
			for range producers {
				go func() {
					defer wg.Done()
					buf := make([]cache.Entry, 0, 16)
					for range perProducer / 16 {
						for range 16 {
							i := lane.Add(1) - 1
							buf = append(buf, cache.Entry{Path: path.Path{}, Weight: i})
						}
						stack.push(buf)
						buf = buf[:0]
					}
				}()
			}
			go func() {
				wg.Wait()
				stack.close()
			}()

			var got []uint64
			scratch := make([]cache.Entry, 0, 16)
			for {
				var ok bool
				scratch, ok = stack.pop(scratch[:0])
				if !ok {
					break
				}
				for _, e := range scratch {
					got = append(got, e.Weight)
				}
			}
			slices.Sort(got)
			assert.Equal(t, want, got, "every record delivered exactly once at capacity %d", tt.capacity)
		})
	}
}

// LIFO is the single claim order of the contract (specs/counter.md, ADR-020):
// on one fixed push/pop sequence the stack hands out the newest entries first.
// Every pop reuses the same scratch buffer and pushes reuse freed slots, as
// consumers do.
func TestTaskStackClaimsLIFO(t *testing.T) {
	const capacity, batch = 4, 2
	stack := newTaskStack(capacity, batch)
	scratch := make([]cache.Entry, 0, batch)

	claim := func() []uint64 {
		var ok bool
		scratch, ok = stack.pop(scratch[:0])
		if !ok {
			return nil
		}
		assert.LessOrEqual(t, len(scratch), batch, "a claim never exceeds the batch size")
		return weightsOf(scratch)
	}

	stack.push(entriesOf(1, 2, 3)) // three of four slots live
	claims := make([][]uint64, 0, 3)
	claims = append(claims, claim(), claim())
	stack.push(entriesOf(4, 5)) // reuses the slots freed by the odd claim
	claims = append(claims, claim())
	stack.close()

	assert.Equal(t, [][]uint64{{2, 3}, {1}, {4, 5}}, claims)
	assert.Nil(t, claim(), "closed and drained ends the consumer loop")
}

// entriesOf builds a flush batch from weights, paths irrelevant to ordering.
func entriesOf(weights ...uint64) []cache.Entry {
	out := make([]cache.Entry, len(weights))
	for i, w := range weights {
		out[i] = cache.Entry{Weight: w}
	}
	return out
}

// weightsOf flattens the weights of a claimed batch.
func weightsOf(batch []cache.Entry) []uint64 {
	out := make([]uint64, len(batch))
	for i, e := range batch {
		out[i] = e.Weight
	}
	return out
}

// cancelSpyMonitor cancels its context the first time any paths are reported
// — i.e. right after the first task of the counting phase was counted (gen
// phases never report paths).
type cancelSpyMonitor struct {
	*monitoring.FakeMonitor
	cancel context.CancelFunc
}

// ReportPathsFound forwards to the fake and cancels the run (idempotent; the
// first call is the one that matters).
func (m *cancelSpyMonitor) ReportPathsFound(count int) {
	m.FakeMonitor.ReportPathsFound(count)
	m.cancel()
}

// Cancelling exactly at the first counted task leaves a partial total without
// a panic: the stack is drained without counting, so with a single consumer
// exactly one task completes (specs/counter.md).
func TestCancelDuringCountingGivesPartialTotal(t *testing.T) {
	c := NewCounter(graph.New(5))
	full := c.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), 1, 6)
	require.Equal(t, uint64(1728), full)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &cancelSpyMonitor{FakeMonitor: monitoring.NewFakeMonitor(), cancel: cancel}

	var got uint64
	assert.NotPanics(t, func() {
		got = c.ParallelCountWithDepth(ctx, spy, 1, 6)
	})
	assert.LessOrEqual(t, got, full, "a partial total never exceeds the full one")
	counting := spy.Phase("counting")
	assert.Equal(t, uint64(1), counting.Completed, "the stack entries after the first counted task are dropped without counting")
}
