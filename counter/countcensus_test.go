package counter

import (
	"context"
	"fmt"
	"iter"
	"math"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"testing"
	"time"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/internal/memtest"
	"knighttour/monitoring"
	"knighttour/types"
	"knighttour/workerpool"

	"github.com/stretchr/testify/assert"
)

// Sampling count-phase census (plan 20): build the real task-cache, then count a
// deterministic sample of class tasks under per-task time budgets. Count-DFS
// reads only the sealed View and never writes back, so task costs are
// independent and the sample is unbiased; the abort fraction at the budget is
// the heavy tail of the cost distribution.

const (
	// censusSampleTasks is the fixed sample size per census point.
	censusSampleTasks = 4096
	// censusTaskBudget bounds one sampled task; exceeded tasks count as aborted.
	censusTaskBudget = 10 * time.Second
)

// censusLadder thresholds for reporting the fraction of tasks exceeding a cost.
var censusLadder = []time.Duration{time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second}

// countCensusStats aggregates one sampled census point.
type countCensusStats struct {
	completed  []time.Duration
	sampled    int
	totalTasks uint64
	aborted    int
	sumNanos   time.Duration
	hits       uint64
	misses     uint64
	totalPaths uint64
	genA       time.Duration
	genB       time.Duration
}

// BenchmarkCountCensus runs the sampling count-phase census (plan 20): the same
// size{N}/depth{D} subtests and -bench selection as the other benchmarks. The
// per-task budget caps every point: even at depths where no task finishes, one
// sample costs at most censusSampleTasks×censusTaskBudget of single-threaded
// time, so no board or depth can run away. peakRSS is a per-process maximum —
// use make bench-count-census for honest numbers on big boards.
func BenchmarkCountCensus(b *testing.B) {
	for _, size := range benchmarkSizes {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			runCensusDepthsCount(b, size, sweepDepths(size))
		})
	}
}

// runCensusDepthsCount runs one board size over the given depths as sampled
// census points. No tour-count verification: a sample never counts all tours.
func runCensusDepthsCount(b *testing.B, size int, depths []int) {
	workers := runtime.NumCPU()
	c := NewCounter(graph.New(size))

	for _, depth := range depths {
		b.Run("depth"+strconv.Itoa(depth), func(b *testing.B) {
			for b.Loop() {
				stats := censusCountOnce(b.Context(), c, workers, depth, censusSampleTasks, censusTaskBudget)
				reportCensusCountMetrics(b, stats)
			}
		})
	}
}

// censusCountOnce runs one census point: the pipeline's generation under the
// configured GC scope (ADR-014), Seal, then the sampled count. sampleN <= 0
// measures every class task; budget <= 0 disables the per-task timeout
// (full-sample equivalence mode).
func censusCountOnce(ctx context.Context, c *Counter, workers, depth, sampleN int, budget time.Duration) countCensusStats {
	if c.gcPercent > 0 {
		prev := debug.SetGCPercent(c.gcPercent)
		defer debug.SetGCPercent(prev)
	}

	m := monitoring.NewFakeMonitor()
	m.Start(ctx)
	intermediate := c.generateIntermediate(ctx, m, workers, depth)
	taskCache := c.extendTasks(ctx, m, workers, intermediate, depth)
	m.Finish() // close the gen B duration before sampling allocates

	view := taskCache.Seal()
	stats := sampleAndCount(ctx, c, view, depth, sampleN, workers, budget)
	stats.genA = m.Phase("gen A").Duration
	stats.genB = m.Phase("gen B").Duration
	return stats
}

// censusSampleOutcome is the disjoint per-task measurement slot written by one
// worker; merging sums nothing across workers.
type censusSampleOutcome struct {
	cost     time.Duration // wall time of a completed task
	aborted  bool
	measured bool // false when ctx ended before this task ran
	hits     uint64
	misses   uint64
	paths    uint64 // weighted; meaningful only with zero aborts
}

// sampleAndCount walks the sealed table twice: the first pass counts class
// tasks and fixes the stride, the second collects every stride-th entry via
// strideSample up to the sample cap (two passes cost minutes even on the
// biggest tables, against hours of counting). Each sampled task is then
// measured once under its own budget in the shared worker pool — task
// independence (the memo is read-only) makes concurrent measurement valid and
// caps the point at sample×budget/workers wall time; aborted tasks keep only
// the fact of exceeding it.
func sampleAndCount(ctx context.Context, c *Counter, view *cache.View, depth, sampleN, workers int, budget time.Duration) countCensusStats {
	var total uint64
	for range view.All(ctx) {
		total++
	}

	sampled := strideSample(ctx, view, total, sampleN)

	outcomes := make([]censusSampleOutcome, len(sampled))
	workerpool.New(workers).Run(ctx, len(sampled), func(ctx context.Context, idx iter.Seq[int]) {
		for i := range idx {
			measureCensusTask(ctx, c, view, depth, budget, sampled[i], &outcomes[i])
		}
	})

	var stats countCensusStats
	stats.totalTasks = total
	for _, o := range outcomes {
		if !o.measured {
			continue // ctx ended before the task ran: not part of the sample
		}
		stats.sampled++
		stats.hits += o.hits
		stats.misses += o.misses
		if o.aborted {
			stats.aborted++
			continue
		}
		stats.completed = append(stats.completed, o.cost)
		stats.sumNanos += o.cost
		stats.totalPaths += o.paths
	}
	return stats
}

// strideSample collects every stride-th record of the sealed table with the
// exact denominator total (stride = ceil(total/sampleN); sampleN ≤ 0 or a
// table that fits the cap collect everything). Unbiased and free of full
// materialization; callers needing a reproducible order sort the result (the
// walk order of a Go map is not one) — see collectMemoSample.
func strideSample(ctx context.Context, view *cache.View, total uint64, sampleN int) []cache.Entry {
	stride := uint64(1)
	if sampleN > 0 && total > uint64(sampleN) {
		stride = (total + uint64(sampleN) - 1) / uint64(sampleN) // ceil: collects ≤ sampleN entries
	}

	sampled := make([]cache.Entry, 0, max(min(int(total), max(sampleN, 0)), 1))
	var i uint64
	for e := range view.All(ctx) {
		if stride == 1 || i%stride == 0 {
			sampled = append(sampled, e)
		}
		i++
	}
	return sampled
}

// measureCensusTask counts one sampled task into its disjoint outcome slot. A
// budgeted task that trips its timeout contributes the abort only: its partial
// path sum is meaningless, so totalPaths stays valid only when aborted stays
// zero (full mode).
func measureCensusTask(ctx context.Context, c *Counter, view *cache.View, depth int, budget time.Duration, e cache.Entry, out *censusSampleOutcome) {
	start := time.Now()
	var res types.Result
	if budget > 0 {
		tctx, cancel := context.WithTimeout(ctx, budget)
		res = c.searcher.CountPathsWithCacheReversal(tctx, e, view, depth)
		out.aborted = tctx.Err() != nil
		cancel()
	} else {
		res = c.searcher.CountPathsWithCacheReversal(ctx, e, view, depth)
	}
	out.cost = time.Since(start)
	out.measured = true
	out.hits = uint64(res.CacheHits)
	out.misses = uint64(res.CacheMisses)
	out.paths = uint64(res.TotalPathsFound) * e.Weight
}

// etaLowDays is the lower-bound full-count time in days: the sample mean cost
// with every aborted task charged exactly the budget, times the class-task
// count, divided by workers. The true tail of aborted tasks is unknown and can
// only push the estimate up.
func (s countCensusStats) etaLowDays(workers int) float64 {
	if s.sampled == 0 {
		return math.NaN()
	}
	meanLB := float64(s.sumNanos+time.Duration(s.aborted)*censusTaskBudget) / float64(s.sampled)
	return meanLB * float64(s.totalTasks) / float64(max(workers, 1)) / float64(24*time.Hour)
}

// abortPct reports the fraction of sampled tasks whose cost exceeds thr:
// aborted tasks (cost > budget ≥ thr) plus completed ones that already took
// longer than thr.
func (s countCensusStats) abortPct(thr time.Duration) float64 {
	if s.sampled == 0 {
		return math.NaN()
	}
	exceeders := s.aborted
	for _, d := range s.completed {
		if d > thr {
			exceeders++
		}
	}
	return 100 * float64(exceeders) / float64(s.sampled)
}

// quantileUs returns the p-quantile of completed task costs in microseconds
// (NaN for an empty completion set; aborted tasks have no measured cost).
func (s countCensusStats) quantileUs(p float64) float64 {
	if len(s.completed) == 0 {
		return math.NaN()
	}
	sorted := make([]time.Duration, len(s.completed))
	copy(sorted, s.completed)
	slices.Sort(sorted)
	idx := min(int(p*float64(len(sorted))), len(sorted)-1)
	return float64(sorted[idx].Microseconds())
}

// reportCensusCountMetrics publishes the sampled census columns (plan 20).
func reportCensusCountMetrics(b *testing.B, s countCensusStats) {
	b.ReportMetric(float64(s.sampled), "sample/op")
	b.ReportMetric(float64(s.totalTasks), "taskEntries/op")
	b.ReportMetric(s.quantileUs(0.50), "p50_us/op")
	b.ReportMetric(s.quantileUs(0.90), "p90_us/op")
	b.ReportMetric(s.quantileUs(0.99), "p99_us/op")
	for _, thr := range censusLadder {
		b.ReportMetric(s.abortPct(thr), fmt.Sprintf("abort%dms_pct/op", thr.Milliseconds()))
	}
	if s.sampled > 0 {
		meanLB := float64(s.sumNanos+time.Duration(s.aborted)*censusTaskBudget) / float64(s.sampled)
		b.ReportMetric(float64(meanLB)/float64(time.Microsecond), "meanLB_us/op")
	}
	b.ReportMetric(s.etaLowDays(runtime.NumCPU()), "etaLowDays/op")
	b.ReportMetric(float64(s.hits), "sampleHits/op")
	b.ReportMetric(float64(s.misses), "sampleMisses/op")
	b.ReportMetric(ms(s.genA), "genA_ms/op")
	b.ReportMetric(ms(s.genB), "genB_ms/op")
	b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
}

// TestCountCensusFullSampleMatchesPipeline pins the harness to the pipeline: a
// full-sample run (stride 1, no budget) must reproduce the reference tour count
// exactly and see exactly as many class tasks as the dispatch counted.
func TestCountCensusFullSampleMatchesPipeline(t *testing.T) {
	cases := []struct {
		size  int
		depth int
	}{
		{5, 6},
		{5, 12},
		{6, 14},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("size%d/depth%d", tc.size, tc.depth), func(t *testing.T) {
			want := toursExpected[tc.size]
			ctx := context.Background()
			c := NewCounter(graph.New(tc.size))

			stats := censusCountOnce(ctx, c, runtime.NumCPU(), tc.depth, 0, 0)

			assert.Zero(t, stats.aborted, "full mode runs without budgets")
			assert.Equal(t, want, stats.totalPaths, "full-sample total must match the reference")

			m := monitoring.NewFakeMonitor()
			m.Start(ctx)
			c.ParallelCountWithDepth(ctx, m, runtime.NumCPU(), tc.depth)
			m.Finish()
			assert.Equal(t, m.Phase("counting").Tasks, stats.totalTasks, "the walk must see every dispatched class task")
		})
	}
}
