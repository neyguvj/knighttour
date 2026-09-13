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
	// task-cache hits/misses, pruning by reason) into the active phase.
	ReportSubtask(r *types.Result)
}

// phaseStats holds counters for a single execution phase (generation, counting).
type phaseStats struct {
	startTime time.Time
	endTime   time.Time
	name      string

	tasks           atomic.Uint64
	completed       atomic.Uint64
	subtasks        atomic.Uint64 // folded ReportSubtask calls (never printed)
	pathsFound      atomic.Uint64 // weighted paths (counting only)
	cacheWrites     atomic.Uint64 // accumulator emissions (gen A / gen B)
	cacheHits       atomic.Uint64 // task-cache lookups answered (reversal counting)
	cacheMisses     atomic.Uint64 // task-cache lookups with no entry (reversal counting)
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

// duration is the phase wall time; zero while the phase is still open.
func (ph *phaseStats) duration() time.Duration {
	if ph.startTime.IsZero() || ph.endTime.IsZero() {
		return 0
	}
	return ph.endTime.Sub(ph.startTime)
}

// monitor is the shared implementation behind both Monitor implementations: it
// tracks phases and counters once, and verbose decides whether the per-second
// live line and the final report are printed. BeginPhase is called strictly
// between phases (workers of the previous phase are done), so switching the
// active phase never races with worker reports.
type monitor struct {
	startTime time.Time
	active    atomic.Pointer[phaseStats]
	phases    []*phaseStats
	phasesMu  sync.Mutex
	started   atomic.Bool
	verbose   bool
}

// RealMonitor is the verbose monitor: live line every second plus final report.
type RealMonitor struct{ monitor }

// FakeMonitor records every report without any console output. Tests and
// benchmarks read the accumulated statistics back via Phase/Totals. Like
// RealMonitor, reports arriving before the first BeginPhase are ignored
// (no active phase).
type FakeMonitor struct{ monitor }

func NewMonitor() *RealMonitor { return &RealMonitor{monitor: monitor{verbose: true}} }

func NewFakeMonitor() *FakeMonitor { return &FakeMonitor{} }

var (
	_ Monitor = (*RealMonitor)(nil)
	_ Monitor = (*FakeMonitor)(nil)
)

// BeginPhase closes the previous phase and starts a new active one. Repeating
// the same name always appends a new phase rather than reusing the old one.
func (m *monitor) BeginPhase(name string) {
	now := time.Now()
	ph := &phaseStats{name: name, startTime: now}

	m.phasesMu.Lock()
	if prev := m.active.Load(); prev != nil {
		prev.endTime = now
	}
	m.phases = append(m.phases, ph)
	m.phasesMu.Unlock()

	m.active.Store(ph)
}

func (m *monitor) AddTasks(count int) {
	if ph := m.active.Load(); ph != nil {
		ph.tasks.Add(uint64(count))
	}
}

func (m *monitor) ReportTaskCompleted() {
	if ph := m.active.Load(); ph != nil {
		ph.completed.Add(1)
	}
}

func (m *monitor) ReportPathsFound(count int) {
	if ph := m.active.Load(); ph != nil {
		ph.pathsFound.Add(uint64(count))
	}
}

// ReportSubtask folds a finished subtask's Result into the active phase.
// TotalPathsFound is intentionally ignored: counting publishes weighted paths
// via ReportPathsFound (Result.TotalPathsFound is unweighted).
func (m *monitor) ReportSubtask(r *types.Result) {
	ph := m.active.Load()
	if ph == nil {
		return
	}
	ph.subtasks.Add(1)
	ph.cacheWrites.Add(uint64(r.CacheWrites))
	ph.cacheHits.Add(uint64(r.CacheHits))
	ph.cacheMisses.Add(uint64(r.CacheMisses))
	ph.prunedDeadEnd.Add(uint64(r.PrunedDeadEnd))
	ph.prunedNoCont.Add(uint64(r.PrunedNoCont))
	ph.prunedDisconn.Add(uint64(r.PrunedDisconn))
	ph.prunedEndpoints.Add(uint64(r.PrunedEndpoints))
}

// Start always anchors the run clock; only the verbose monitor spawns the
// per-second reporter goroutine.
func (m *monitor) Start(ctx context.Context) {
	m.startTime = time.Now()
	m.started.Store(true)
	if !m.verbose {
		return
	}
	go m.loop(ctx)
}

func (m *monitor) loop(ctx context.Context) {
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

// report prints the active phase progress on a single line (\x1b[2K\r-overwritten).
// Conditional segments keep the line free of zeros: Writes appears only when the
// phase wrote cache entries, Hits/Misses only when reversal lookups happened.
// The silent monitor never prints anything.
func (m *monitor) report() {
	if !m.verbose {
		return
	}
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
	b.WriteString(ph.hitsSegment(false))
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
	b.WriteString(ph.hitsSegment(true))
	fmt.Fprintf(&b, " | pruned %d%s", ph.prunedTotal(), ph.pruneBreakdown())
	return b.String()
}

// hitsSegment renders " | Hits hits/misses" once the phase probed the task
// cache (ReportSubtask carries the lookups); printed when at least one of the
// two is non-zero, so lines stay free of zeros. lowercase picks the
// summary-line spelling.
func (ph *phaseStats) hitsSegment(lowercase bool) string {
	hits, misses := ph.cacheHits.Load(), ph.cacheMisses.Load()
	if hits == 0 && misses == 0 {
		return ""
	}
	name := "Hits"
	if lowercase {
		name = "hits"
	}
	return fmt.Sprintf(" | %s %d/%d", name, hits, misses)
}

// pruneBreakdown renders "(deadend N, nocont N, disconn N, endpoints N)" with
// only non-zero parts in this fixed order; empty string when nothing was
// pruned.
func (ph *phaseStats) pruneBreakdown() string {
	counters := [...]struct {
		name string
		v    uint64
	}{
		{"deadend", ph.prunedDeadEnd.Load()},
		{"nocont", ph.prunedNoCont.Load()},
		{"disconn", ph.prunedDisconn.Load()},
		{"endpoints", ph.prunedEndpoints.Load()},
	}
	parts := make([]string, 0, len(counters))
	for _, c := range counters {
		if c.v > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", c.name, c.v))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// Finish closes the active phase for both monitors and prints the final report
// only when verbose. Calling it without Start is a no-op.
func (m *monitor) Finish() {
	if !m.started.Swap(false) {
		return
	}
	m.report()

	m.phasesMu.Lock()
	defer m.phasesMu.Unlock()
	if prev := m.active.Load(); prev != nil && prev.endTime.IsZero() {
		prev.endTime = time.Now()
	}
	if !m.verbose {
		return
	}

	fmt.Printf("\n=== Final ===\n")
	fmt.Printf("Total time: %s\n", time.Since(m.startTime))

	var totalPaths uint64
	for _, ph := range m.phases {
		fmt.Printf("Phase %s [%s]: %s\n", ph.name, ph.duration(), ph.summary())
		totalPaths += ph.pathsFound.Load()
	}
	fmt.Printf("Total paths: %d\n", totalPaths)
}

// PhaseStats is a snapshot of the counters recorded for one phase.
type PhaseStats struct {
	Tasks       uint64
	Completed   uint64
	Subtasks    uint64
	PathsFound  uint64
	CacheWrites uint64

	CacheHits   uint64 // task-cache lookups answered (counting)
	CacheMisses uint64 // task-cache lookups with no entry (counting)

	Pruned          uint64
	PrunedDeadEnd   uint64
	PrunedNoCont    uint64
	PrunedDisconn   uint64
	PrunedEndpoints uint64

	Duration time.Duration // zero while the phase is still open
}

// Phase returns the summed counters of every phase recorded under name; the
// zero value means no such phase started.
func (m *monitor) Phase(name string) PhaseStats {
	m.phasesMu.Lock()
	defer m.phasesMu.Unlock()

	var sum PhaseStats
	for _, ph := range m.phases {
		if ph.name == name {
			sum.add(ph)
		}
	}
	return sum
}

// Totals sums the counters of every recorded phase.
func (m *monitor) Totals() PhaseStats {
	m.phasesMu.Lock()
	defer m.phasesMu.Unlock()

	var sum PhaseStats
	for _, ph := range m.phases {
		sum.add(ph)
	}
	return sum
}

// add folds one phase's live counters into a snapshot (used by Phase/Totals).
func (a *PhaseStats) add(ph *phaseStats) {
	a.Tasks += ph.tasks.Load()
	a.Completed += ph.completed.Load()
	a.Subtasks += ph.subtasks.Load()
	a.PathsFound += ph.pathsFound.Load()
	a.CacheWrites += ph.cacheWrites.Load()
	a.CacheHits += ph.cacheHits.Load()
	a.CacheMisses += ph.cacheMisses.Load()
	a.Pruned += ph.prunedTotal()
	a.PrunedDeadEnd += ph.prunedDeadEnd.Load()
	a.PrunedNoCont += ph.prunedNoCont.Load()
	a.PrunedDisconn += ph.prunedDisconn.Load()
	a.PrunedEndpoints += ph.prunedEndpoints.Load()
	a.Duration += ph.duration()
}
