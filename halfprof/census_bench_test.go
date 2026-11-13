package halfprof

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"knighttour/internal/memtest"
)

// BenchmarkHalfprofCensus measures one endpoint-group run of the half-profile
// pipeline (plan 30): points are named size{N}/g{s}-{t}/{leaf} covering every D4
// orbit representative; leaf is p{P} for the bucket-pass counts in censusPasses
// or x{MB} for the external-merge spill thresholds in censusSpillMBs. The -bench
// filter alone decides which points run (ADR-020, specs/benchmarks.md
// «Полупрофильный ценз»); 7×7 and 8×8 are heavy per group and run only behind
// an explicit make bench-halfprof, one point per process for honest peakRSS_MB/op.
// p1 is the single-pass census; p{P>1} counts the group in Chernov's multi-pass
// bucket mode (summed table sizes plus the average held per pass); x{MB} counts it
// on the external-merge path and reports spills/op and diskPeakMB/op. The dp leaf
// runs the profile-DP engine of plan 31 (boards 5–7) and publishes its layered
// state census; an over-cap cutoff is a SKIP carrying the partial layer log.
func BenchmarkHalfprofCensus(b *testing.B) {
	for _, size := range []int{5, 6, 7, 8} {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			for _, gr := range Groups(size) {
				b.Run(fmt.Sprintf("g%d-%d", gr.S, gr.T), func(b *testing.B) {
					if structurallyZeroGroup(size, gr.S, gr.T) {
						b.Skipf("endpoints %d,%d cannot bound a tour on size %d (coloring)", gr.S, gr.T, size)
					}
					for _, passes := range censusPasses {
						b.Run("p"+strconv.Itoa(passes), func(b *testing.B) {
							runGroupCensusPoint(b, size, gr.S, gr.T, passes)
						})
					}
					for _, spillMB := range censusSpillMBs {
						b.Run("x"+strconv.Itoa(spillMB), func(b *testing.B) {
							runGroupExternalPoint(b, size, gr.S, gr.T, spillMB)
						})
					}
					if size <= 7 {
						b.Run("dp", func(b *testing.B) {
							runDpCensusPoint(b, size, gr.S, gr.T)
						})
					}
				})
			}
		})
	}
}

// censusPasses are the bucket-pass counts each group point sweeps: p1 pins the
// single-pass baseline, the rest show the per-pass memory fall of the bucketed
// mode (specs/benchmarks.md «Полупрофильный ценз»).
var censusPasses = []int{1, 2, 4, 8}

// censusSpillMBs are the external-path spill thresholds each group point sweeps,
// in MiB: they trade one enumeration for disk runs and expose how spills/op and
// the on-disk peak fall as the threshold grows (specs/benchmarks.md).
var censusSpillMBs = []int{1, 8, 64}

// structurallyZeroGroup applies the coloring obstruction of Hamiltonian paths:
// a tour alternates cell colors, so on an even board its endpoints lie on
// opposite colors and on an odd board both on the majority color. Every other
// orbit has no tours at all, and its point runs nothing (the full-board 8×8
// table therefore carries only the differently-colored representatives —
// specs/benchmarks.md).
func structurallyZeroGroup(n, s, t int) bool {
	cs, ct := (s/n+s%n)%2, (t/n+t%n)%2
	if n%2 == 0 {
		return cs == ct
	}
	return cs != ct || cs != 0
}

// runGroupCensusPoint measures one group at one pass count: the summed census
// counters across passes, their per-pass average (what a single bucket holds)
// and the resident peak.
func runGroupCensusPoint(b *testing.B, size, s, t, passes int) {
	for b.Loop() {
		start := time.Now()
		total, cen, err := CountGroupWithPasses(context.Background(), size, s, t, passes)
		if err != nil {
			b.Fatalf("size%d g%d-%d p%d: %v", size, s, t, passes, err)
		}
		b.ReportMetric(float64(time.Since(start).Microseconds())/1e3, "group_ms/op")
		b.ReportMetric(float64(passes), "passes/op")
		b.ReportMetric(float64(cen.ProfilesU), "profilesU/op")
		b.ReportMetric(float64(cen.ProfilesL), "profilesL/op")
		b.ReportMetric(float64(cen.ProfilesL)/float64(passes), "profilesL_pass/op")
		b.ReportMetric(float64(cen.Joins), "joins/op")
		b.ReportMetric(float64(cen.MaxProfileCount), "maxProfile/op")
		b.ReportMetric(float64(total), "count/op")
		b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
	}
}

// runDpCensusPoint measures one group with the profile-DP engine (plan 31
// step 0): both halves walk layer by layer, every layer row goes to the log
// (half, step, live states, considered transitions), and the peaks become
// metrics — layerPeak/op is the gate number of the step, trans/op counts the
// considered edge subsets, bytesPerState/op divides the reserved-table peak
// by the state peak, keyWords/op publishes the packed key width and
// maxWeight/op witnesses the uint64 counter width. count/op runs through the
// untouched join and must equal the enumerator census. An over-cap cutoff is
// a SKIP; its message and the already-logged layers carry the progress.
func runDpCensusPoint(b *testing.B, size, s, t int) {
	g := newCutGeom(size)
	dg, ok := newDpGeom(g, s, t)
	if !ok {
		b.Fatalf("size%d g%d-%d: DP geometry does not fit the key layout", size, s, t)
	}
	for b.Loop() {
		start := time.Now()
		upTab := newAggTable("upper", &memBudget{})
		cenU, err := runHalfDpGeom(&dg, context.Background(), false, 0, 1, &memBudget{}, upTab)
		logDpLayers(b, cenU)
		failOrSkipDpCensus(b, err)
		loTab := newAggTable("lower", &memBudget{})
		cenL, err := runHalfDpGeom(&dg, context.Background(), true, 0, 1, &memBudget{}, loTab)
		logDpLayers(b, cenL)
		failOrSkipDpCensus(b, err)

		total, _, err := joinTables(context.Background(), sortedEntries(upTab.m), sortedEntries(loTab.m))
		if err != nil {
			b.Fatalf("size%d g%d-%d dp join: %v", size, s, t, err)
		}
		b.ReportMetric(float64(time.Since(start).Microseconds())/1e3, "dp_ms/op")
		b.ReportMetric(float64(max(cenU.LayerPeak, cenL.LayerPeak)), "layerPeak/op")
		b.ReportMetric(float64(cenU.Transitions+cenL.Transitions), "trans/op")
		b.ReportMetric(dpBytesPerState(cenU, cenL), "bytesPerState/op")
		b.ReportMetric(float64(dg.kl.kw), "keyWords/op")
		b.ReportMetric(float64(max(cenU.MaxWeight, cenL.MaxWeight)), "maxWeight/op")
		b.ReportMetric(float64(total), "count/op")
		b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
	}
}

// logDpLayers writes the per-layer census of one half to the bench log as a
// single line (benchmark logs are capped at ~1 KiB by cmd/go): "step:states"
// and "step:transitions" sequences, peak steps first in plain text.
func logDpLayers(b *testing.B, cen DpCensus) {
	states := make([]string, 0, len(cen.Layers))
	trans := make([]string, 0, len(cen.Layers))
	for _, l := range cen.Layers {
		states = append(states, fmt.Sprintf("%d:%d", l.Step, l.States))
		trans = append(trans, fmt.Sprintf("%d:%d", l.Step, l.Trans))
	}
	b.Logf("dp %s peak=%d bytes=%d states[%s] trans[%s]",
		cen.Side, cen.LayerPeak, cen.PeakBytes, strings.Join(states, ","), strings.Join(trans, ","))
}

// dpBytesPerState returns the larger per-state reserved-byte ratio of the two
// halves (flat-table bytes at the layer peak, load cap included).
func dpBytesPerState(cenU, cenL DpCensus) float64 {
	ratio := func(cen DpCensus) float64 {
		if cen.LayerPeak == 0 {
			return 0
		}
		return float64(cen.PeakBytes) / float64(cen.LayerPeak)
	}
	return max(ratio(cenU), ratio(cenL))
}

// failOrSkipDpCensus records an over-cap cutoff as a SKIP whose message
// carries the cutoff progress (layers already logged), and fails any other
// error (ADR-046 lesson: the harness must tolerate the soft cap).
func failOrSkipDpCensus(b *testing.B, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, ErrOverCap) {
		b.Skipf("profile-DP soft cap cutoff: %v", err)
	}
	b.Fatalf("profile-DP census failed: %v", err)
}

// runGroupExternalPoint measures one group on the external-merge path at a spill
// threshold in MiB: wall time, the number of runs spilled during enumeration and
// the peak on-disk bytes of live runs (the disk the ceiling must cover). count/op
// must equal the in-memory census for the same group.
func runGroupExternalPoint(b *testing.B, size, s, t, spillMB int) {
	opts := ExternalOptions{SpillBytes: spillMB << 20, Dir: b.TempDir()}
	for b.Loop() {
		start := time.Now()
		total, st, err := CountGroupExternal(context.Background(), size, s, t, opts)
		if err != nil {
			b.Fatalf("size%d g%d-%d x%dMB: %v", size, s, t, spillMB, err)
		}
		b.ReportMetric(float64(time.Since(start).Microseconds())/1e3, "group_ms/op")
		b.ReportMetric(float64(st.Spills), "spills/op")
		b.ReportMetric(float64(st.DiskPeakBytes)/(1<<20), "diskPeakMB/op")
		b.ReportMetric(float64(total), "count/op")
		b.ReportMetric(float64(memtest.PeakRSSBytes())/(1<<20), "peakRSS_MB/op")
	}
}
