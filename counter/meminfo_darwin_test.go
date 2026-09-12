//go:build darwin

package counter

import "syscall"

// peakRSSOS returns rusage maxrss in bytes (darwin reports bytes natively).
func peakRSSOS() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return uint64(ru.Maxrss)
}
