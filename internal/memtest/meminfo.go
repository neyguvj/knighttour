// Package memtest provides process-memory probes for benchmark and census
// tests: the per-process resident-set peak (rusage maxrss) and the cumulative
// allocation counter of the Go runtime. It is imported from test code only, so
// nothing here links into a production binary. Peak RSS never decreases within
// a process — when several measured points share one test binary it reflects
// the hungriest of them; run one point per process for meaningful numbers.
package memtest

import "runtime"

// PeakRSSBytes returns the process-wide resident set peak in bytes.
func PeakRSSBytes() uint64 { return peakRSSOS() }

// TotalAllocBytes returns the cumulative bytes allocated by the process since
// start (monotonic). Deltas around a measured section capture allocation churn
// reproducibly, unlike the RSS peak.
func TotalAllocBytes() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}
