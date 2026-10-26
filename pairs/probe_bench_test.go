package pairs

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"knighttour/graph"
	"knighttour/internal/memtest"
)

// BenchmarkPairsProbe confirms a matrix row N(s,·) of the pairs package with
// the independent meet-in-the-middle probe (plan 28): points are named
// size{N}/k{M} with M the cut boundary; the point table lives here and the
// -bench filter alone decides which points run (ADR-020, specs/benchmarks.md
// «Зонд попарного счёта»). The probe row must match Row on every end — a
// divergence fails the point. peakRSS_MB/op is a per-process maximum — run one
// board per process (make bench-pairs-probe).
func BenchmarkPairsProbe(b *testing.B) {
	for _, size := range slices.Sorted(maps.Keys(probePoints)) {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			g := graph.New(size)
			for _, m := range probePoints[size] {
				b.Run("k"+strconv.Itoa(m), func(b *testing.B) {
					runPairsProbePoint(b, g, probeStart, m)
				})
			}
		})
	}
}

// BenchmarkPairsRowCost publishes the reference row/matrix cost of plan 28:
// wall time of one Row and of the full Compute matrix on 6×6, to be compared
// with the reversal pipeline's 6×6 run (~54 ms) — how many pipeline runs a
// row costs. Not a probe point; run directly:
// go test -run='^$' -bench=BenchmarkPairsRowCost -benchtime=1x ./pairs/
func BenchmarkPairsRowCost(b *testing.B) {
	g := graph.New(6)
	ctx := context.Background()
	b.Run("row", func(b *testing.B) {
		for b.Loop() {
			var sum uint64
			for _, w := range Row(ctx, g, probeStart) {
				sum += w
			}
			if sum == 0 {
				b.Fatal("empty row")
			}
		}
	})
	b.Run("matrix", func(b *testing.B) {
		for b.Loop() {
			if Compute(ctx, g, 4).Total() == 0 {
				b.Fatal("empty matrix")
			}
		}
	})
}

// probeStart is the anchored start cell of every probe point: the corner, a
// cell with tours on both boards.
const probeStart = 0

// probePoints are the plan-28 cut boundaries per board: the balanced m for
// 5×5 and 6×6 (m = ⌈cells/2⌉). Heavy points run under ../kt-bench.lock like
// other long points; 7×7 is out — its row costs orders of work beyond any
// census budget.
var probePoints = map[int][]int{
	5: {13},
	6: {18},
}

// runPairsProbePoint measures one probe point: the expected row is anchored
// once outside the timed loop (deterministic, so a per-iteration re-anchor
// would only double the price), each iteration runs the MITM probe, verifies
// it end by end and publishes the census columns of specs/benchmarks.md.
func runPairsProbePoint(b *testing.B, g *graph.Graph, start, m int) {
	want := Row(context.Background(), g, start)
	for b.Loop() {
		startTime := time.Now()
		got, cen := mitmProbe(context.Background(), g, start, m)
		dur := time.Since(startTime)

		if !slices.Equal(got, want) {
			b.Fatalf("size%d k%d: MITM probe diverges from Row", g.Size(), m)
		}
		b.ReportMetric(float64(cen.walksP), "walksP/op")
		b.ReportMetric(float64(cen.walksT), "walksT/op")
		b.ReportMetric(float64(cen.entries), "entries/op")
		b.ReportMetric(float64(dur.Microseconds())/1e3, "probe_ms/op")
		b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
	}
}
