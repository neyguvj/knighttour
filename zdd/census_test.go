package zdd

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"knighttour/internal/memtest"
)

// ZDD live-node census (plan 23): how many live nodes the reduced diagram of a
// knight-board Hamiltonian family occupies and what building it costs. Point
// naming, metric meanings and the frontier-DP comparison base live in
// specs/benchmarks.md «Ценз живых узлов ZDD»; the -bench filter alone decides
// which points run (ADR-020).

// zddCensusOrder selects the edge order a census point builds with.
type zddCensusOrder uint8

const (
	orderRow    zddCensusOrder = iota // nil order: the sequence the edges are listed in
	orderNarrow                       // OrderNarrow's deterministic narrow search
)

// String renders the order kind as its subtest name segment.
func (o zddCensusOrder) String() string {
	if o == orderNarrow {
		return "narrow"
	}
	return "row"
}

// zddCensusPoint is one census point: family, edge order and the directed-tour
// oracle of docs/requirements.md compared as 2×Count(). Every tabled point has
// an oracle (the 5×5–7×7 references are complete), so each one verifies.
type zddCensusPoint struct {
	fam          Family
	order        zddCensusOrder
	wantDirected uint64
}

// zddCensusPoints are the plan-23 points per board. The row controls exist only
// on 5×5 and 6×6: they measure the gain of the order search, while a bad order
// on 7×7 could eat hours under the bench lock for a point that is never the
// reference (specs/benchmarks.md).
var zddCensusPoints = map[int][]zddCensusPoint{
	5: {
		{Paths, orderRow, 1_728},
		{Paths, orderNarrow, 1_728},
		{Cycles, orderRow, 0},
		{Cycles, orderNarrow, 0},
	},
	6: {
		{Paths, orderRow, 6_637_920},
		{Paths, orderNarrow, 6_637_920},
		{Cycles, orderRow, 19_724},
		{Cycles, orderNarrow, 19_724},
	},
	7: {
		{Paths, orderNarrow, 165_575_218_320},
		{Cycles, orderNarrow, 0},
	},
}

// BenchmarkZDDCensus measures the live-node census of the ZDD builder as
// size{N}/{paths|cycles}/{row|narrow} subtests from zddCensusPoints. The build
// is single-threaded and deterministic: node counts and weights reproduce
// bit-exactly across processes, only wall time floats. peakRSS_MB/op is a
// per-process maximum — run one board per process (make bench-zdd-census).
func BenchmarkZDDCensus(b *testing.B) {
	for _, size := range slices.Sorted(maps.Keys(zddCensusPoints)) {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			nVerts, edges := knightEdges(b, size)
			for _, pt := range zddCensusPoints[size] {
				b.Run(famName(pt.fam)+"/"+pt.order.String(), func(b *testing.B) {
					runZDDCensusPoint(b, nVerts, edges, pt)
				})
			}
		})
	}
}

// runZDDCensusPoint builds the diagram once per iteration (the census targets
// -benchtime=1x), verifies the directed-tour oracle and publishes the census
// columns of specs/benchmarks.md. An over-cap abort is a SKIP carrying the
// cutoff progress; any other build error fails the point.
func runZDDCensusPoint(b *testing.B, nVerts int, edges [][2]int, pt zddCensusPoint) {
	order := censusOrder(pt.order, nVerts, edges) // search cost stays out of census_ms
	for b.Loop() {
		start := time.Now()
		d, err := Build(nVerts, edges, pt.fam, order)
		dur := time.Since(start)
		if err != nil {
			failOrSkipZDDCensus(b, err)
			return
		}
		if got := directedTours(d.Count()); got != pt.wantDirected {
			b.Fatalf("%s/%s: got %d directed tours, want %d", famName(pt.fam), pt.order, got, pt.wantDirected)
		}
		b.ReportMetric(float64(d.LiveNodes()), "nodes/op")
		b.ReportMetric(float64(d.LevelPeak()), "levelPeak/op")
		b.ReportMetric(float64(dur.Microseconds())/1e3, "census_ms/op")
		b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
	}
}

// censusOrder resolves the edge order of a point once before timing: row is
// nil (Build's documented default), narrow runs OrderNarrow outside the timed
// loop because its deterministic search cost must not enter census_ms.
func censusOrder(order zddCensusOrder, nVerts int, edges [][2]int) []int {
	if order == orderNarrow {
		return OrderNarrow(nVerts, edges)
	}
	return nil
}

// failOrSkipZDDCensus records an over-cap cutoff as a SKIP whose message
// carries the live-node progress (specs/benchmarks.md), and fails any other
// build error.
func failOrSkipZDDCensus(b *testing.B, err error) {
	var over *OverCapError
	if errors.As(err, &over) {
		b.Skipf("arena cap cutoff: %v", err) // the error text reports the live-node count
	}
	b.Fatalf("zdd census build failed: %v", err)
}

// famName renders a family as its census subtest name segment.
func famName(fam Family) string {
	if fam == Cycles {
		return "cycles"
	}
	return "paths"
}
