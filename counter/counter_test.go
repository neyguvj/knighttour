package counter

import (
	"context"
	"runtime/metrics"
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
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

		groupAcc := cache.NewAccumulator()
		sink := groupAcc.Local()
		result := counter.searcher.GenerateRoots(ctx, sink, group.Canonical, uint64(group.OrbitSize), base)
		sink.Flush()

		totalWeight := uint64(0)
		for _, e := range groupAcc.Drain() {
			assert.Positive(t, e.Weight, "weight must be positive")
			totalWeight += e.Weight
		}

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
	assert.Equal(t, 10, DefaultPrecomputeDepth(6))
	assert.Equal(t, 20, DefaultPrecomputeDepth(7))
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
