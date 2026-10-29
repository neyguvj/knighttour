package pairs

import (
	"context"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"knighttour/graph"
	"knighttour/internal/memtest"
	"knighttour/pruner"
	"knighttour/symmetry"
)

// BenchmarkPairsCensus measures the time profile of one anchored row descent
// (plan 29): points are named size{N}/row/s{cell}/t{budget} with the start a
// D4 orbit representative and the budget a {t6h, t24h} segment applied as a
// context deadline. The point table lives here and the -bench filter alone
// decides which points run (ADR-020, specs/benchmarks.md «Ценз попарного
// разложения»). Progress prints to stdout as CSV lines `t_s,nodes,leaves`
// directly under the point's Benchmark header. Cheap boards (≤ 6×6) verify the
// histogram against Row; peakRSS_MB/op is a per-process maximum — run one
// point per process (make bench-pairs-census).
func BenchmarkPairsCensus(b *testing.B) {
	for _, size := range slices.Sorted(maps.Keys(censusRowPoints)) {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			g := graph.New(size)
			for _, p := range censusRowPoints[size] {
				b.Run(fmt.Sprintf("row/s%d/%s", p.cell, p.budget.name), func(b *testing.B) {
					runRowCensusPoint(b, g, p)
				})
			}
		})
	}
}

// BenchmarkPairsHalfCensus measures the half-path capacity curve of the MITM
// sides with the existing mitmWalk (plan 29): points are named size{N}/pfx{k}
// (prefix side, pruner cutoffs, corner anchor) and size{N}/sfx{k} (suffix
// side without cutoffs, anchored on the first parity-legal end representative),
// k being the cut boundary in cells including the anchor. Boundaries run in
// ascending order and the walk stops once the process peak RSS crosses the
// ~28 GB ceiling of plan 29; a point that exhausts its time budget publishes
// the reached counters with timedOut/op = 1 (specs/benchmarks.md «Ценз
// попарного разложения»). One point per process for honest peakRSS_MB/op.
func BenchmarkPairsHalfCensus(b *testing.B) {
	for _, size := range slices.Sorted(maps.Keys(censusHalfPoints)) {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			g := graph.New(size)
			runHalfSide(b, g, "pfx", censusHalfPoints[size].prefixes, true)
			runHalfSide(b, g, "sfx", censusHalfPoints[size].suffixes, false)
		})
	}
}

// censusTickNodes is the work tick of the row profile points: ctx and the
// wall-clock mark are evaluated every 2^20 visited nodes, keeping clock reads
// off the per-node path while bounding stop latency far below a day budget.
const censusTickNodes = 1 << 20

// censusEmitEvery is the wall interval between time-kind marks of a row point;
// power-of-two node marks fire deterministically on top.
const censusEmitEvery = time.Minute

// halfPointBudget bounds one half-census walk by wall time: an unpruned suffix
// side past its depth budget would run days, so the reached counters are
// published as a lower bound with timedOut/op = 1 instead.
const halfPointBudget = 6 * 60 * time.Minute

// halfRSSCeiling is the ~28 GB resident peak ceiling of plan 29: once crossed,
// the boundary walk stops and the remaining boundaries skip.
const halfRSSCeiling = uint64(28) << 30

// censusBudget is one segment budget of a row profile point.
type censusBudget struct {
	name    string
	timeout time.Duration
}

// censusBudgets are the plan-29 segments named in the point pattern t{budget}.
var censusBudgets = []censusBudget{
	{"t6h", 6 * time.Hour},
	{"t24h", 24 * time.Hour},
}

// rowCensusPoint is one profile point: an anchored start cell and its budget.
type rowCensusPoint struct {
	budget censusBudget
	cell   int
}

// halfCensusBoundaries are the ascending cut boundaries of one board side.
type halfCensusBoundaries struct {
	prefixes []int
	suffixes []int
}

// censusRowPoints is the row profile point table per board (ADR-020): cheap
// boards carry the corner anchor only, 8×8 carries every D4 orbit
// representative at both segment budgets — heavy points run only behind an
// explicit -bench filter, one process each.
var censusRowPoints = map[int][]rowCensusPoint{
	5: rowCensusPoints([]int{probeStart}),
	6: rowCensusPoints([]int{probeStart}),
	8: rowCensusPoints(d4Representatives(8)),
}

// censusHalfPoints is the half-path boundary table per board: 5×5 and 6×6 sit
// at the balanced probe cut of plan 28 (prefix m, suffix cells−m), 8×8 walks
// the ascending curve from depth 10 to the half-board 32.
var censusHalfPoints = map[int]halfCensusBoundaries{
	5: {prefixes: []int{13}, suffixes: []int{12}},
	6: {prefixes: []int{18}, suffixes: []int{18}},
	8: {prefixes: evenRange(10, 32), suffixes: evenRange(10, 32)},
}

// rowCensusPoints expands the start cells over all segment budgets.
func rowCensusPoints(cells []int) []rowCensusPoint {
	pts := make([]rowCensusPoint, 0, len(cells)*len(censusBudgets))
	for _, c := range cells {
		for _, bd := range censusBudgets {
			pts = append(pts, rowCensusPoint{cell: c, budget: bd})
		}
	}
	return pts
}

// evenRange returns the ascending even boundaries from lo to hi inclusive.
func evenRange(lo, hi int) []int {
	out := make([]int, 0, hi/2-lo/2+1)
	for k := lo; k <= hi; k += 2 {
		out = append(out, k)
	}
	return out
}

// d4Representatives returns the canonical start cells of the D4 orbits in
// ascending order — the row anchors of the matrix census.
func d4Representatives(size int) []int {
	groups := symmetry.NewSymmetry(size).GetCanonicalGroups()
	reps := make([]int, 0, len(groups))
	for _, gr := range groups {
		reps = append(reps, gr.Canonical)
	}
	return reps
}

// endRepCell returns the first D4 orbit representative that parity allows as a
// path end from probeStart — the single anchor of the suffix-side census.
func endRepCell(g *graph.Graph) int {
	majority := majorityColor(g)
	for _, rep := range d4Representatives(g.Size()) {
		if !parityForbids(g, majority, probeStart, rep) {
			return rep
		}
	}
	return probeStart
}

// runRowCensusPoint measures one profile point: each iteration runs the
// instrumented descent under the segment budget, prints the progress marks and
// publishes the counters; cheap boards also anchor the histogram against Row.
func runRowCensusPoint(b *testing.B, g *graph.Graph, p rowCensusPoint) {
	var want []uint64
	if g.Size() <= 6 { // heavy boards cannot afford the Row anchor
		want = Row(context.Background(), g, p.cell)
	}
	for b.Loop() {
		ctx, stop := context.WithTimeout(context.Background(), p.budget.timeout)
		startTime := time.Now()
		hist, nodes, leaves := rowCensus(ctx, g, p.cell, censusHooks{
			tickNodes: censusTickNodes,
			interval:  censusEmitEvery,
			emit: func(elapsed time.Duration, n, l uint64) {
				fmt.Printf("%.3f,%d,%d\n", elapsed.Seconds(), n, l)
			},
		})
		dur := time.Since(startTime)
		stop()

		if want != nil && !slices.Equal(hist, want) {
			b.Fatalf("size%d row/s%d: census histogram diverges from Row", g.Size(), p.cell)
		}
		b.ReportMetric(float64(nodes), "nodes/op")
		b.ReportMetric(float64(leaves), "leaves/op")
		b.ReportMetric(float64(dur.Microseconds())/1e3, "row_ms/op")
		b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
	}
}

// runHalfSide walks one side's boundaries ascending and stops the walk once a
// point crossed the RSS ceiling; the remaining boundaries report SKIP. The
// side table of each point is released before the next boundary so the process
// footprint tracks one boundary even though peak RSS stays monotone.
func runHalfSide(b *testing.B, g *graph.Graph, side string, bounds []int, usePruner bool) {
	anchor := probeStart
	if !usePruner {
		anchor = endRepCell(g)
	}
	ceiled := false
	for _, k := range bounds {
		b.Run(side+strconv.Itoa(k), func(b *testing.B) {
			if ceiled {
				b.Skipf("previous %s boundary crossed the RSS ceiling", side)
			}
			runHalfPoint(b, g, anchor, k, usePruner)
			if memtest.PeakRSSBytes() > halfRSSCeiling {
				ceiled = true
				b.Logf("%s%d: peak RSS crossed the plan-29 ceiling — walk stops", side, k)
			}
		})
		runtime.GC()
	}
}

// runHalfPoint measures one boundary of one side: the walk runs under the
// point budget and publishes half-paths, side-table records and peak RSS.
func runHalfPoint(b *testing.B, g *graph.Graph, anchor, depth int, usePruner bool) {
	for b.Loop() {
		ctx, stop := context.WithTimeout(context.Background(), halfPointBudget)
		var p *pruner.Pruner
		if usePruner {
			p = pruner.New(g)
		}
		side := mitmWalk(ctx, g, p, anchor, depth)
		timedOut := 0.0
		if ctx.Err() != nil {
			timedOut = 1
			b.Logf("budget %v exhausted — counters are a lower bound", halfPointBudget)
		}
		stop()

		b.ReportMetric(float64(side.walks), "walks/op")
		b.ReportMetric(float64(side.records()), "entries/op")
		b.ReportMetric(timedOut, "timedOut/op")
		b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
	}
}
