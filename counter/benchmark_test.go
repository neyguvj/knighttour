package counter

import (
	"context"
	"runtime"
	"strconv"
	"testing"
	"time"

	"knighttour/graph"
	"knighttour/monitoring"
)

// benchmarkSizes are the boards the depth sweep covers, biggest cut first.
// Which of them actually run is decided solely by the -bench filter of the
// Makefile target (ADR-020): `make bench` → size[56], `bench-deep` →
// size[567], `bench-8x8` → size8; a direct `go test -bench=.` runs every
// board (hours/days).
var benchmarkSizes = []int{5, 6, 7, 8}

// depthFloors is the lowest swept depth per board size (default 1). On 7×7 a
// point at the floor costs ~50 min (count-phase dominated) and grows downward,
// so the sweep stops there to bound `make bench-deep` runtime.
var depthFloors = map[int]int{7: 6}

// toursExpected is the number of open tours (all symmetries counted) per board
// size — the invariant every split depth must reproduce. Sizes absent from the
// table are measured but not verified (exploratory runs, e.g. the first 8×8).
var toursExpected = map[int]uint64{
	5: 1_728,
	6: 6_637_920,
	7: 165_575_218_320,
}

// BenchmarkCountAllTours sweeps every meet-in-the-middle split root per board
// size, descending (size²/2..floor): deep cuts are the expensive and the
// interesting ones on big boards, so they run first. Metrics are published next
// to ns/op and split by phase — per-phase wall time (genA/genB/cnt ms),
// generation footprint (writes/pruned of gen A and gen B), task-cache lookups
// of counting (cacheHits/cacheMisses) and memory (peakRSS_MB/op,
// totalAllocMB/op).
//
// peakRSS is a process-wide maximum: run one board size per process
// (`make bench-size N=…`), otherwise it reflects the most hungry subtest.
func BenchmarkCountAllTours(b *testing.B) {
	for _, size := range benchmarkSizes {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			runDepths(b, size, sweepDepths(size))
		})
	}
}

// sweepDepths returns the depth list for a board size: the full descending
// sweep from size²/2 down to the floor. The code selects no depths itself —
// point runs are anchored `^depth(a|b)$` -bench filters in the Makefile and
// the 8×8 {32,30} cap is only their default when DEPTHS is unset (ADR-020);
// a filter matching no existing depth simply executes nothing.
func sweepDepths(size int) []int {
	return descending(size*size/2, depthFloor(size))
}

// runDepths runs one board size over the given depths as `depth{D}` subtests,
// checking the tour count and publishing per-phase metrics.
func runDepths(b *testing.B, size int, depths []int) {
	workers := runtime.NumCPU()
	c := NewCounter(graph.New(size))
	want, verify := toursExpected[size]

	for _, depth := range depths {
		b.Run("depth"+strconv.Itoa(depth), func(b *testing.B) {
			for b.Loop() {
				// Fresh monitor per iteration: its snapshots are additive.
				// Start/Finish close the last phase so its duration is readable.
				m := monitoring.NewFakeMonitor()
				ctx := context.Background()
				alloc0 := totalAllocBytes()
				m.Start(ctx)
				got := c.ParallelCountWithDepth(ctx, m, workers, depth)
				m.Finish()
				peak := peakRSSBytes() // read before reporting: allocation below must not count
				totalAlloc := totalAllocBytes() - alloc0
				if verify && got != want {
					b.Fatalf("size%d depth%d: got %d tours, want %d", size, depth, got, want)
				}
				reportBenchMetrics(b, m)
				b.ReportMetric(float64(peak)/(1<<20), "peakRSS_MB/op")
				b.ReportMetric(float64(totalAlloc)/(1<<20), "totalAllocMB/op")
			}
		})
	}
}

// descending returns from..to (inclusive), e.g. 4..2 → [4 3 2].
func descending(from, to int) []int {
	d := make([]int, max(from-to+1, 0))
	for i := range d {
		d[i] = from - i
	}
	return d
}

// depthFloor returns the lowest swept depth for a board size (1 unless tabled).
func depthFloor(size int) int {
	if f, ok := depthFloors[size]; ok {
		return f
	}
	return 1
}

// reportBenchMetrics publishes the per-phase counters collected by FakeMonitor.
func reportBenchMetrics(b *testing.B, m *monitoring.FakeMonitor) {
	genA := m.Phase("gen A")
	genB := m.Phase("gen B")
	counting := m.Phase("counting")

	b.ReportMetric(float64(counting.Duration.Microseconds())/1e3, "cnt_ms/op")
	b.ReportMetric(ms(genA.Duration), "genA_ms/op")
	b.ReportMetric(ms(genB.Duration), "genB_ms/op")
	b.ReportMetric(float64(genA.CacheWrites), "writesA/op")
	b.ReportMetric(float64(genB.CacheWrites), "writesB/op")
	b.ReportMetric(float64(genA.Pruned), "prunedA/op")
	b.ReportMetric(float64(genB.Pruned), "prunedB/op")
	// Task-cache lookups of the counting phase at the reversal stop level.
	b.ReportMetric(float64(counting.CacheHits), "cacheHits/op")
	b.ReportMetric(float64(counting.CacheMisses), "cacheMisses/op")
}

// ms renders a phase duration as milliseconds with microsecond precision.
func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1e3 }
