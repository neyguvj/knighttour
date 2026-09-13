package counter

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/pruner"
)

// benchmarkSizes are the boards the depth sweep covers, biggest cut first.
var benchmarkSizes = []int{5, 6, 7, 8}

// deepEnvVar gates the slow boards (gatedSizes): without it their subtests skip,
// so `make bench` stays cheap. `make bench-deep` sets it.
const deepEnvVar = "BENCH_DEEP"

// sizeEnvVars overrides the gate variable per size (7 keeps deepEnvVar; 8×8 has
// its own because one point there is expected to cost hours, plan 05).
var sizeEnvVars = map[int]string{8: "BENCH_8X8"}

// gatedSizes need their gate env var: one measurement costs minutes to more than an hour.
var gatedSizes = map[int]bool{7: true, 8: true}

// gateVar returns the env var gating a board size (deepEnvVar unless overridden).
func gateVar(size int) string {
	if v, ok := sizeEnvVars[size]; ok {
		return v
	}
	return deepEnvVar
}

// depthFloors is the lowest split depth per board size (default 1). Below depth
// 10 on 7×7 the final DP over huge complements runs hours (depth 9 ≈ 8.8h vs 22min
// at 10) and depth 6 was OOM-killed, so the sweep stops there.
var depthFloors = map[int]int{7: 6}

// benchDepthsEnv overrides the swept depths (comma separated, e.g. "30,32") for
// point measurements of hour-long cuts; values outside [floor, size²/2] are dropped.
const benchDepthsEnv = "BENCH_DEPTHS"

// benchModeEnv selects the counting mode ("class" | "reversal", default
// class) so one benchmark measures either pipeline for the A/B sweep (plan 06).
const benchModeEnv = "BENCH_MODE"

// benchMode resolves the counting mode from the environment; unknown values
// fail fast instead of silently measuring the wrong pipeline.
func benchMode(b *testing.B) Mode {
	switch v := os.Getenv(benchModeEnv); v {
	case "", "class":
		return ModeClass
	case "reversal":
		return ModeReversal
	default:
		b.Fatalf("%s: unknown mode %q (want class|reversal)", benchModeEnv, v)
		return ModeClass
	}
}

// sweepDefaults caps the 8×8 default sweep: a full descent from depth 32 is many
// hours per point, so without BENCH_DEPTHS only these run until the first real
// measurements fix a proper floor (plan 05).
var sweepDefaults = map[int][]int{8: {32, 30}}

// toursExpected is the number of open tours (all symmetries counted) per board
// size — the invariant every split depth must reproduce. Sizes absent from the
// table are measured but not verified (exploratory runs, e.g. the first 8×8).
var toursExpected = map[int]uint64{
	5: 1_728,
	6: 6_637_920,
	7: 165_575_218_320,
}

// BenchmarkCountAllToursClass sweeps every meet-in-the-middle split root per
// board size, descending (size²/2..floor): deep cuts are the expensive and the
// interesting ones on big boards, so they run first. Metrics are published next
// to ns/op and split by phase — per-phase wall time (genA/genB/cnt ms),
// generation footprint (writes/pruned of gen A and gen B), final-pass totals
// (classes/shapes/zeros; reversal counting: cacheHits/cacheMisses) and memory
// (peakRSS_MB/op, totalAllocMB/op). BENCH_MODE selects the pipeline
// (class|reversal, default class — plan 06 A/B); 8×8 is measured in reversal
// mode only, its class subtests skip (ADR-015).
//
// peakRSS is a process-wide maximum: run one board size per process
// (`make bench-size N=…`), otherwise it reflects the most hungry subtest.
func BenchmarkCountAllToursClass(b *testing.B) {
	for _, size := range benchmarkSizes {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			if size == 8 && benchMode(b) == ModeClass {
				b.Skip("class mode on 8x8 is not measured (ADR-015): it cannot fit in memory; use BENCH_MODE=reversal")
			}
			if gate := gateVar(size); gatedSizes[size] && os.Getenv(gate) != "1" {
				b.Skipf("set %s=1: one %d×%d measurement costs ~9 min..hours", gate, size, size)
			}
			runDepths(b, size, sweepDepths(b, size))
		})
	}
}

// sweepDepths resolves the depth list for a board size: BENCH_DEPTHS override
// (clamped to [floor, size²/2]) or the descending default / tabled cap.
func sweepDepths(b *testing.B, size int) []int {
	floor, ceiling := depthFloor(size), size*size/2
	if spec := os.Getenv(benchDepthsEnv); spec != "" {
		var depths []int
		for s := range strings.SplitSeq(spec, ",") {
			d, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				b.Fatalf("%s: bad depth %q", benchDepthsEnv, s)
			}
			if d >= floor && d <= ceiling {
				depths = append(depths, d)
			}
		}
		if len(depths) == 0 {
			b.Skipf("%s=%q selects no depth for size%d (valid [%d, %d])", benchDepthsEnv, spec, size, floor, ceiling)
		}
		return depths
	}
	if def, ok := sweepDefaults[size]; ok {
		return def
	}
	return descending(ceiling, floor)
}

// runDepths runs one board size over the given depths as `depth{D}` subtests,
// checking the tour count and publishing per-phase metrics.
func runDepths(b *testing.B, size int, depths []int) {
	workers := runtime.NumCPU()
	c := NewCounter(graph.New(size))
	c.SetMode(benchMode(b)) // BENCH_MODE=class|reversal (default class)
	if os.Getenv("SHAPE_FILTER") == "off" {
		c.SetShapeFilter(pruner.L2None) // A/B measurement of the plan-02 filter
	}
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
	classes, shapes, zeros := m.ShapeStats()

	b.ReportMetric(float64(m.Phase("counting").Duration.Microseconds())/1e3, "cnt_ms/op")
	b.ReportMetric(ms(genA.Duration), "genA_ms/op")
	b.ReportMetric(ms(genB.Duration), "genB_ms/op")
	b.ReportMetric(float64(classes), "classes/op")
	b.ReportMetric(float64(shapes), "shapes/op")
	b.ReportMetric(float64(zeros), "zeros/op")
	b.ReportMetric(float64(genA.CacheWrites), "writesA/op")
	b.ReportMetric(float64(genB.CacheWrites), "writesB/op")
	b.ReportMetric(float64(genA.Pruned), "prunedA/op")
	b.ReportMetric(float64(genB.Pruned), "prunedB/op")
	counting := m.Phase("counting")
	b.ReportMetric(float64(counting.PrunedArticulation), "prunedDP_artic/op")
	b.ReportMetric(float64(counting.PrunedForcedChain), "prunedDP_chain/op")
	b.ReportMetric(float64(counting.FilteredShapes), "filtered/op")
	// Reversal task-cache lookups of the counting phase (zero in class mode).
	b.ReportMetric(float64(counting.CacheHits), "cacheHits/op")
	b.ReportMetric(float64(counting.CacheMisses), "cacheMisses/op")
}

// ms renders a phase duration as milliseconds with microsecond precision.
func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1e3 }
