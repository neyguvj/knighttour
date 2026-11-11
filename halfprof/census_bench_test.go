package halfprof

import (
	"context"
	"fmt"
	"strconv"
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
// on the external-merge path and reports spills/op and diskPeakMB/op.
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
