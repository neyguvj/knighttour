package split

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"

	"knighttour/internal/memtest"
)

// Split census (plan 26): how many interface states σ, what layer peak and what
// gluing cost the half-board cut pays per board and family. Point naming, metric
// meanings and the frontier-DP comparison base live in specs/benchmarks.md
// «Ценз разреза пополам со склейкой»; the -bench filter alone decides which
// points run (ADR-020).

// splitCensusPoint is one census point: family, seam variant, the directed-tour
// oracle of docs/requirements.md compared as 2×Count(), and a per-point byte-cap
// override (capBytes = 0 keeps the package default; heavy points that need more
// than the default lever raise it explicitly — tests moving the internal lever
// directly is the sanctioned mechanism, there are no env handles).
type splitCensusPoint struct {
	fam          Family
	v            Variant
	wantDirected uint64
	capBytes     int
}

// splitCensusPoints are the plan-26 points per board: straight always, mirror
// for even n (the D4-compressed cut is only defined there).
var splitCensusPoints = map[int][]splitCensusPoint{
	5: {
		{Paths, Straight, 1_728, 0},
		{Cycles, Straight, 0, 0},
	},
	6: {
		{Paths, Straight, 6_637_920, 0},
		{Paths, Mirror, 6_637_920, 0},
		{Cycles, Straight, 19_724, 0},
		{Cycles, Mirror, 19_724, 0},
	},
	7: {
		{Paths, Straight, 165_575_218_320, 0},
		{Cycles, Straight, 0, 0},
	},
}

// BenchmarkSplitCensus measures the half-cut census as size{N}/{paths|cycles}/
// {straight|mirror} subtests from splitCensusPoints. The count is single-threaded
// and deterministic: totals and every counter reproduce bit-exactly across
// processes, only wall time floats. peakRSS_MB/op is a per-process maximum — run
// one board per process (make bench-split-census).
func BenchmarkSplitCensus(b *testing.B) {
	for _, size := range slices.Sorted(maps.Keys(splitCensusPoints)) {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			for _, pt := range splitCensusPoints[size] {
				b.Run(famName(pt.fam)+"/"+variantName(pt.v), func(b *testing.B) {
					runSplitCensusPoint(b, size, pt)
				})
			}
		})
	}
}

// runSplitCensusPoint counts once per iteration (the census targets
// -benchtime=1x), verifies the directed-tour oracle and publishes the census
// columns of specs/benchmarks.md. An over-cap abort is a SKIP carrying the
// cutoff progress; any other error fails the point.
func runSplitCensusPoint(b *testing.B, size int, pt splitCensusPoint) {
	if pt.capBytes != 0 {
		restore := setSoftCapForTest(pt.capBytes)
		defer restore()
	}
	for b.Loop() {
		total, cen, tim, err := countInternal(size, pt.fam, pt.v)
		if err != nil {
			failOrSkipSplitCensus(b, err)
			return
		}
		if got := 2 * total; got != pt.wantDirected {
			b.Fatalf("%s/%s: got %d directed tours, want %d", famName(pt.fam), variantName(pt.v), got, pt.wantDirected)
		}
		b.ReportMetric(float64(cen.SigmaL), "sigmaL/op")
		b.ReportMetric(float64(cen.SigmaR), "sigmaR/op")
		b.ReportMetric(float64(cen.LayerPeak), "layerPeakHalf/op")
		b.ReportMetric(float64(cen.Joins), "joins/op")
		b.ReportMetric(float64(cen.MaxWeight), "maxWeight/op")
		b.ReportMetric(float64(tim.dp.Microseconds())/1e3, "dp_ms/op")
		b.ReportMetric(float64(tim.join.Microseconds())/1e3, "join_ms/op")
		b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
	}
}

// failOrSkipSplitCensus records an over-cap cutoff as a SKIP whose message
// carries the progress counters (specs/benchmarks.md), and fails any other error.
func failOrSkipSplitCensus(b *testing.B, err error) {
	var over *OverCapError
	if errors.As(err, &over) {
		b.Skipf("soft cap cutoff: %v", err) // the error text reports layer and states
	}
	b.Fatalf("split census count failed: %v", err)
}
