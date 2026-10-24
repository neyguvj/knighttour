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

// ZDD half census (plan 27): how many memo entries, nodes and σ states an
// open-seam half build pays per board and family, and what the glued total
// costs. Point naming, metric meanings and the whole-board comparison base
// live in specs/benchmarks.md «Ценз ZDD половин»; the -bench filter alone
// decides which points run (ADR-020).

// zddHalfCensusPoint is one census point: family, seam variant (mirror = one
// build serving both halves through the reflection ρ, even n only) and the
// directed-tour oracle of docs/requirements.md compared as 2×Glue.
type zddHalfCensusPoint struct {
	fam          Family
	mirror       bool
	wantDirected uint64
}

// zddHalfCensusPoints are the plan-27 points per board: straight cuts always,
// mirror for paths on even n (specs/benchmarks.md «Ценз ZDD половин»).
var zddHalfCensusPoints = map[int][]zddHalfCensusPoint{
	5: {
		{Paths, false, 1_728},
		{Cycles, false, 0},
	},
	6: {
		{Paths, false, 6_637_920},
		{Paths, true, 6_637_920},
		{Cycles, false, 19_724},
	},
	7: {
		{Paths, false, 165_575_218_320},
		{Cycles, false, 0},
	},
}

// BenchmarkZDDHalfCensus measures the half census as size{N}/{paths|cycles}
// [/mirror] subtests from zddHalfCensusPoints. The build and the σ pass are
// single-threaded and deterministic: counts, tables and totals reproduce
// bit-exactly across processes, only wall time floats. peakRSS_MB/op is a
// per-process maximum — run one board per process (make bench-zdd-half-census).
func BenchmarkZDDHalfCensus(b *testing.B) {
	for _, size := range slices.Sorted(maps.Keys(zddHalfCensusPoints)) {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			gl, gr, seam := boardHalves(size)
			for _, pt := range zddHalfCensusPoints[size] {
				name := famName(pt.fam)
				if pt.mirror {
					name += "/mirror"
				}
				b.Run(name, func(b *testing.B) {
					runZDDHalfCensusPoint(b, size, gl, gr, seam, pt)
				})
			}
		})
	}
}

// runZDDHalfCensusPoint runs one session (builds + σ pass + glue) per
// iteration (the census targets -benchtime=1x), verifies the directed-tour
// oracle and publishes the census columns of specs/benchmarks.md. An over-cap
// abort is a SKIP carrying the cutoff progress; any other error fails the point.
func runZDDHalfCensusPoint(b *testing.B, size int, gl, gr halfGeom, seam [][2]int, pt zddHalfCensusPoint) {
	perm := mirrorPerm(size, seam)
	if pt.mirror && perm == nil {
		b.Fatalf("mirror cut needs even n, got %d", size)
	}
	ordL, ordR := gl.narrowOrder(), gr.narrowOrder() // order search stays out of build_ms
	for b.Loop() {
		sess := NewHalfSession(pt.fam)

		t0 := time.Now()
		left, err := sess.BuildHalf(gl.nVerts, gl.edges, gl.stubOf, gl.edgeIDs, ordL)
		var right *HalfDiagram
		var rSig map[Sigma]uint64
		if err == nil && !pt.mirror {
			right, err = sess.BuildHalf(gr.nVerts, gr.edges, gr.stubOf, gr.edgeIDs, ordR)
		}
		buildDur := time.Since(t0)
		if err != nil {
			failOrSkipZDDHalfCensus(b, err)
			return
		}

		t1 := time.Now()
		lSig, err := left.Sigmas()
		if err == nil && !pt.mirror {
			var errR error
			rSig, errR = right.Sigmas()
			if errR != nil {
				err = errR
			}
		}
		if err != nil {
			failOrSkipZDDHalfCensus(b, err)
			return
		}
		if pt.mirror {
			rSig = reflectSigmas(lSig, perm)
		}
		distDur := time.Since(t1)

		t2 := time.Now()
		total, joins := Glue(lSig, rSig, pt.fam)
		joinDur := time.Since(t2)
		if got := 2 * total; got != pt.wantDirected {
			b.Fatalf("%s mirror=%v: got %d directed tours, want %d", famName(pt.fam), pt.mirror, got, pt.wantDirected)
		}

		reportZDDHalfCensus(b, sess, left, right, lSig, rSig, joins, buildDur, distDur, joinDur)
	}
}

// reportZDDHalfCensus publishes the metric columns of the point. The mirror
// variant has no second diagram: its right-half columns repeat the left ones
// (one table serves both sides), and nodesShared is then the full half width.
func reportZDDHalfCensus(b *testing.B, sess *HalfSession, left, right *HalfDiagram, lSig, rSig map[Sigma]uint64, joins uint64, buildDur, distDur, joinDur time.Duration) {
	memoR, nodesR := left.b.memo.memoEntries, left.nodes
	if right != nil {
		memoR, nodesR = right.b.memo.memoEntries, right.nodes
	}
	nodesShared := sharedNodes(sess, left, right)
	liveMB := float64(left.registryBytes()+sigmaTableBytes(lSig)+sigmaTableBytes(rSig)) / (1 << 20)

	b.ReportMetric(float64(left.b.memo.memoEntries), "memoL/op")
	b.ReportMetric(float64(memoR), "memoR/op")
	b.ReportMetric(float64(left.nodes), "nodesL/op")
	b.ReportMetric(float64(nodesR), "nodesR/op")
	b.ReportMetric(float64(nodesShared), "nodesShared/op")
	b.ReportMetric(liveMB, "liveBytesMB/op")
	b.ReportMetric(float64(len(lSig)), "sigmaL/op")
	b.ReportMetric(float64(len(rSig)), "sigmaR/op")
	b.ReportMetric(float64(distSum(left, right)), "distEntries/op")
	b.ReportMetric(float64(joins), "joins/op")
	b.ReportMetric(float64(maxWeight(lSig, rSig)), "maxWeight/op")
	b.ReportMetric(float64(buildDur.Microseconds())/1e3, "build_ms/op")
	b.ReportMetric(float64(distDur.Microseconds())/1e3, "dist_ms/op")
	b.ReportMetric(float64(joinDur.Microseconds())/1e3, "join_ms/op")
	b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
}

// distSum reports the σ-pass intermediate sizes: both halves for a straight
// cut, one half for the mirror (its right table is a reflection, not a pass).
func distSum(left, right *HalfDiagram) uint64 {
	if right == nil {
		return left.distEntries
	}
	return left.distEntries + right.distEntries
}

// sharedNodes counts session nodes reachable from both half roots — the node
// reuse the shared unique table buys across the two builds. The mirror variant
// has no second diagram and reports the full half width as shared.
func sharedNodes(sess *HalfSession, left, right *HalfDiagram) int {
	if right == nil {
		return left.nodes
	}
	lSet := reachIDs(sess, left)
	rSet := reachIDs(sess, right)
	if len(rSet) > len(lSet) {
		lSet, rSet = rSet, lSet
	}
	shared := 0
	for id := range lSet {
		if _, ok := rSet[id]; ok {
			shared++
		}
	}
	return shared
}

// reachIDs walks the session arena from one root and returns the reachable
// non-terminal node ids (terminals excluded on both sides, so the count is a
// fair intersection).
func reachIDs(sess *HalfSession, h *HalfDiagram) map[uint32]bool {
	set := make(map[uint32]bool, h.nodes)
	stack := []uint32{h.root}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id < numTerminals || set[id] {
			continue
		}
		set[id] = true
		n := &sess.a.nodes[id]
		stack = append(stack, n.low, n.high)
	}
	return set
}

// maxWeight returns the largest σ weight over both tables — the uint64-width
// evidence column of the census.
func maxWeight(tables ...map[Sigma]uint64) uint64 {
	var best uint64
	for _, t := range tables {
		for _, w := range t {
			best = max(best, w)
		}
	}
	return best
}

// failOrSkipZDDHalfCensus records an over-cap cutoff as a SKIP whose message
// carries the progress counters (specs/benchmarks.md), and fails any other
// error.
func failOrSkipZDDHalfCensus(b *testing.B, err error) {
	var over *OverCapError
	if errors.As(err, &over) {
		b.Skipf("soft cap cutoff: %v", err) // the error text reports nodes and bytes
	}
	b.Fatalf("zdd half census failed: %v", err)
}
