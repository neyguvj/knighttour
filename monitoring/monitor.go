package monitoring

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"knighttour/types"
)

// clearLine erases the whole current terminal line and returns the caret,
// so each per-second report fully overwrites the previous one.
const clearLine = "\x1b[2K\r"

type Monitor interface {
	Start(ctx context.Context)
	Finish()
	BeginPhase(name string)
	AddTasks(count int)
	ReportTaskCompleted()
	ReportPathsFound(count int)
	// ReportSubtask folds one finished subtask's statistics (cache writes,
	// reversal hits/misses, pruning by reason) into the active phase.
	ReportSubtask(r types.Result)
	// ReportOracleStats publishes shape-oracle totals once after counting;
	// zeros counts classes with no routes found (h == 0).
	ReportOracleStats(lookups, computes, classes, zeros int)
}

// phaseStats holds counters for a single execution phase (generation, counting).
type phaseStats struct {
	startTime       time.Time
	endTime         time.Time
	name            string
	tasks           atomic.Uint64
	completed       atomic.Uint64
	pathsFound      atomic.Uint64 // weighted paths (counting only)
	cacheWrites     atomic.Uint64 // cache.Set calls (generation phases)
	cacheHits       atomic.Uint64 // reversal-lookup hits (counting, legacy cache mode)
	cacheMisses     atomic.Uint64 // reversal-lookup misses (counting, legacy cache mode)
	prunedDeadEnd   atomic.Uint64
	prunedNoCont    atomic.Uint64
	prunedDisconn   atomic.Uint64
	prunedEndpoints atomic.Uint64
}

// prunedTotal is the sum of the per-reason pruning counters.
func (ph *phaseStats) prunedTotal() uint64 {
	return ph.prunedDeadEnd.Load() + ph.prunedNoCont.Load() +
		ph.prunedDisconn.Load() + ph.prunedEndpoints.Load()
}

// RealMonitor tracks per-phase progress with lock-free counters. BeginPhase is
// called strictly between phases (workers of the previous phase are done), so
// switching the active phase never races with worker reports.
type RealMonitor struct {
	startTime time.Time
	active    atomic.Pointer[phaseStats]
	phases    []*phaseStats
	phasesMu  sync.Mutex
	started   atomic.Bool

	oracleSet      atomic.Bool
	oracleLookups  atomic.Uint64
	oracleComputes atomic.Uint64
	oracleClasses  atomic.Uint64
	oracleZeros    atomic.Uint64 // classes with h == 0 (no routes found)
}

func NewMonitor() *RealMonitor {
	return &RealMonitor{}
}

var (
	_ Monitor = (*RealMonitor)(nil)
	_ Monitor = (*FakeMonitor)(nil)
)

// BeginPhase closes the previous phase and starts a new active one.
func (m *RealMonitor) BeginPhase(name string) {
	if prev := m.active.Load(); prev != nil {
		prev.endTime = time.Now()
	}
	ph := &phaseStats{name: name, startTime: time.Now()}
	m.phasesMu.Lock()
	m.phases = append(m.phases, ph)
	m.phasesMu.Unlock()
	m.active.Store(ph)
}

func (m *RealMonitor) AddTasks(count int) {
	if ph := m.active.Load(); ph != nil {
		ph.tasks.Add(uint64(count))
	}
}

func (m *RealMonitor) Start(ctx context.Context) {
	m.startTime = time.Now()
	m.started.Store(true)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if !m.started.Load() {
					return
				}
				m.report()
			case <-ctx.Done():
				// Finish() already printed the final report; only report here
				// if it never ran (e.g. context cancelled mid-flight).
				if m.started.Load() {
					m.report()
				}
				return
			}
		}
	}()
}

// estimateRemaining is a linear ETA for the active phase based on its average
// task rate. ok == false means the estimate is unknown (no task completed yet
// or total task count not known).
func estimateRemaining(elapsed time.Duration, completed, total uint64) (time.Duration, bool) {
	if completed == 0 || total == 0 {
		return 0, false
	}
	if completed >= total {
		return 0, true
	}
	return elapsed * time.Duration(total-completed) / time.Duration(completed), true
}

// fmtDur formats a duration with millisecond precision (e.g. "1.234s").
func fmtDur(d time.Duration) string { return d.Round(time.Millisecond).String() }

// hitRate is the percentage of successful lookups; callers must guard against
// a zero denominator (no lookups at all).
func hitRate(hits, misses uint64) float64 {
	return float64(hits) / float64(hits+misses) * 100
}

// report prints the active phase progress on a single line (\x1b[2K\r-overwritten).
// Conditional segments keep the line free of zeros: Writes appears only when the
// phase wrote cache entries, Hits/Misses only when reversal lookups happened.
func (m *RealMonitor) report() {
	ph := m.active.Load()
	if ph == nil {
		return
	}

	completed := ph.completed.Load()
	totalTasks := ph.tasks.Load()

	pct := 0.0
	if totalTasks > 0 {
		pct = float64(completed) / float64(totalTasks) * 100
	}

	// ETA is estimated from the active phase elapsed time; ph.startTime is
	// published via active.Store, so reading it here happens-after and is race-free.
	remaining, ok := estimateRemaining(time.Since(ph.startTime), completed, totalTasks)
	eta := "--"
	if ok {
		eta = fmtDur(remaining)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[%s] Phase %s | Tasks: %d/%d (%.1f%%) | Paths %d",
		fmtDur(time.Since(m.startTime)), ph.name, completed, totalTasks, pct, ph.pathsFound.Load())
	if w := ph.cacheWrites.Load(); w > 0 {
		fmt.Fprintf(&b, " | Writes %d", w)
	}
	hits, misses := ph.cacheHits.Load(), ph.cacheMisses.Load()
	if hits+misses > 0 {
		fmt.Fprintf(&b, " | Hits %d (%.1f%%) Misses %d", hits, hitRate(hits, misses), misses)
	}
	fmt.Fprintf(&b, " | Pruned %d | ETA %s", ph.prunedTotal(), eta)

	fmt.Print(clearLine + b.String())
}

// summary renders the phase line of the final report: tasks/paths plus the
// same conditional cache segments and a pruning breakdown listing only the
// non-zero reasons.
func (ph *phaseStats) summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tasks %d/%d | paths %d", ph.completed.Load(), ph.tasks.Load(), ph.pathsFound.Load())
	if w := ph.cacheWrites.Load(); w > 0 {
		fmt.Fprintf(&b, " | writes %d", w)
	}
	hits, misses := ph.cacheHits.Load(), ph.cacheMisses.Load()
	if hits+misses > 0 {
		fmt.Fprintf(&b, " | hits %d (%.1f%%) misses %d", hits, hitRate(hits, misses), misses)
	}
	fmt.Fprintf(&b, " | pruned %d%s", ph.prunedTotal(), ph.pruneBreakdown())
	return b.String()
}

// pruneBreakdown renders "(deadend N, nocont N, disconn N, endpoints N)" with
// only non-zero parts; empty string when nothing was pruned.
func (ph *phaseStats) pruneBreakdown() string {
	parts := make([]string, 0, 4)
	if v := ph.prunedDeadEnd.Load(); v > 0 {
		parts = append(parts, fmt.Sprintf("deadend %d", v))
	}
	if v := ph.prunedNoCont.Load(); v > 0 {
		parts = append(parts, fmt.Sprintf("nocont %d", v))
	}
	if v := ph.prunedDisconn.Load(); v > 0 {
		parts = append(parts, fmt.Sprintf("disconn %d", v))
	}
	if v := ph.prunedEndpoints.Load(); v > 0 {
		parts = append(parts, fmt.Sprintf("endpoints %d", v))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func (m *RealMonitor) Finish() {
	if !m.started.Swap(false) {
		return
	}
	m.report()
	if prev := m.active.Load(); prev != nil {
		prev.endTime = time.Now()
	}

	fmt.Printf("\n=== Final ===\n")
	fmt.Printf("Total time: %s\n", time.Since(m.startTime))

	var totalPaths uint64
	for _, ph := range m.phases {
		fmt.Printf("Phase %s [%s]: %s\n", ph.name, ph.endTime.Sub(ph.startTime), ph.summary())
		totalPaths += ph.pathsFound.Load()
	}
	if m.oracleSet.Load() {
		fmt.Printf("Oracle: lookups=%d computes=%d classes=%d zeros=%d\n",
			m.oracleLookups.Load(), m.oracleComputes.Load(),
			m.oracleClasses.Load(), m.oracleZeros.Load())
	}
	fmt.Printf("Total paths: %d\n", totalPaths)
}

func (m *RealMonitor) ReportTaskCompleted() {
	if ph := m.active.Load(); ph != nil {
		ph.completed.Add(1)
	}
}

func (m *RealMonitor) ReportPathsFound(count int) {
	if ph := m.active.Load(); ph != nil {
		ph.pathsFound.Add(uint64(count))
	}
}

// ReportSubtask folds a finished subtask's Result into the active phase.
// TotalPathsFound is intentionally ignored: counting publishes weighted paths
// via ReportPathsFound (Result.TotalPathsFound is unweighted).
func (m *RealMonitor) ReportSubtask(r types.Result) {
	ph := m.active.Load()
	if ph == nil {
		return
	}
	ph.cacheWrites.Add(uint64(r.CacheWrites))
	ph.cacheHits.Add(uint64(r.CacheHits))
	ph.cacheMisses.Add(uint64(r.CacheMisses))
	ph.prunedDeadEnd.Add(uint64(r.PrunedDeadEnd))
	ph.prunedNoCont.Add(uint64(r.PrunedNoCont))
	ph.prunedDisconn.Add(uint64(r.PrunedDisconn))
	ph.prunedEndpoints.Add(uint64(r.PrunedEndpoints))
}

func (m *RealMonitor) ReportOracleStats(lookups, computes, classes, zeros int) {
	m.oracleLookups.Store(uint64(lookups))
	m.oracleComputes.Store(uint64(computes))
	m.oracleClasses.Store(uint64(classes))
	m.oracleZeros.Store(uint64(zeros))
	m.oracleSet.Store(true)
}

type FakeMonitor struct{}

func NewFakeMonitor() *FakeMonitor {
	return &FakeMonitor{}
}

func (*FakeMonitor) Start(ctx context.Context)                               {}
func (*FakeMonitor) Finish()                                                 {}
func (*FakeMonitor) BeginPhase(name string)                                  {}
func (*FakeMonitor) AddTasks(count int)                                      {}
func (*FakeMonitor) ReportTaskCompleted()                                    {}
func (*FakeMonitor) ReportPathsFound(count int)                              {}
func (*FakeMonitor) ReportSubtask(r types.Result)                            {}
func (*FakeMonitor) ReportOracleStats(lookups, computes, classes, zeros int) {}
