//go:build linux

package counter

import "syscall"

// peakRSSOS returns rusage maxrss converted to bytes; on linux the kernel
// reports kilobytes, on darwin bytes — the ×1024 trap TestPeakRSSUnits guards.
func peakRSSOS() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return uint64(ru.Maxrss) * 1024
}
