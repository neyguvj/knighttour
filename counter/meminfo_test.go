package counter

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// peakRSSBytes returns the process-wide resident set peak in bytes.
//
// IMPORTANT: this is a per-process maximum that never decreases — when several
// board sizes run in one benchmark process the value corresponds to the most
// memory-hungry subtest, not to the current iteration. Use `make bench-size N=…`
// (one process per board) for meaningful numbers; see specs/counter.md.
func peakRSSBytes() uint64 { return peakRSSOS() }

// totalAllocBytes returns the cumulative bytes allocated by the process since
// start (monotonic). Deltas around a benchmark iteration measure allocation
// churn reproducibly, unlike the RSS peak.
func totalAllocBytes() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}

const peakRSSChildEnv = "PEAKRSS_CHILD"

// TestPeakRSSUnits guards the darwin/linux unit mismatch (rusage Maxrss is in
// bytes on darwin, kilobytes on linux). It runs a fresh child process — inside
// the shared test binary Maxrss is a historical maximum already inflated by
// other tests, so only a virgin process gives a meaningful band. A ×1024
// mistake lands far outside [chunk, 8GiB] in either direction.
func TestPeakRSSUnits(t *testing.T) {
	const chunk = 64 << 20 // 64 MiB

	if os.Getenv(peakRSSChildEnv) == "1" {
		leak := make([]byte, chunk)
		for i := range leak {
			leak[i] = byte(i)
		}
		peak := peakRSSBytes()
		runtime.KeepAlive(leak)
		fmt.Println("peak:", peak)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestPeakRSSUnits$")
	cmd.Env = append(os.Environ(), peakRSSChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if i := strings.LastIndex(string(out), "peak: "); i >= 0 {
		rest := string(out)[i+len("peak: "):]
		peak, perr := strconv.ParseUint(strings.Fields(rest)[0], 10, 64)
		if perr != nil {
			t.Fatalf("cannot parse child peak from %q: %v", rest, perr)
		}
		if peak < chunk {
			t.Fatalf("child peak RSS %d bytes after allocating %d: under-reported (missing ×1024 on linux? dead measurement?)", peak, chunk)
		}
		if peak > 8<<30 {
			t.Fatalf("child peak RSS %d bytes after allocating %d: implausible (×1024 unit error?)", peak, chunk)
		}
		return
	}
	t.Fatalf("child did not report its peak RSS (err=%v):\n%s", err, out)
}
