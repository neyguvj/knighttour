package monitoring

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
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
	ReportCacheWrites(count int)
	ReportPruned(count int)
}

// phaseStats holds counters for a single execution phase (generation, counting).
type phaseStats struct {
	startTime   time.Time
	endTime     time.Time
	name        string
	tasks       atomic.Uint64
	completed   atomic.Uint64
	pathsFound  atomic.Uint64
	cacheWrites atomic.Uint64
	pruned      atomic.Uint64
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

// report prints the active phase progress on a single line (\x1b[2K\r-overwritten).
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

	fmt.Printf(
		clearLine+"[%s] Phase %s | Tasks: %d/%d (%.1f%%) | Paths %d | Writes %d | Pruned %d | ETA %s",
		fmtDur(time.Since(m.startTime)), ph.name,
		completed, totalTasks, pct,
		ph.pathsFound.Load(),
		ph.cacheWrites.Load(),
		ph.pruned.Load(),
		eta,
	)
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
		fmt.Printf(
			"Phase %s [%s]: tasks %d/%d | paths %d | writes %d | pruned %d\n",
			ph.name, ph.endTime.Sub(ph.startTime),
			ph.completed.Load(), ph.tasks.Load(),
			ph.pathsFound.Load(), ph.cacheWrites.Load(), ph.pruned.Load(),
		)
		totalPaths += ph.pathsFound.Load()
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

func (m *RealMonitor) ReportCacheWrites(count int) {
	if ph := m.active.Load(); ph != nil {
		ph.cacheWrites.Add(uint64(count))
	}
}

func (m *RealMonitor) ReportPruned(count int) {
	if ph := m.active.Load(); ph != nil {
		ph.pruned.Add(uint64(count))
	}
}

type FakeMonitor struct{}

func NewFakeMonitor() *FakeMonitor {
	return &FakeMonitor{}
}

func (*FakeMonitor) Start(ctx context.Context)   {}
func (*FakeMonitor) Finish()                     {}
func (*FakeMonitor) BeginPhase(name string)      {}
func (*FakeMonitor) AddTasks(count int)          {}
func (*FakeMonitor) ReportTaskCompleted()        {}
func (*FakeMonitor) ReportPathsFound(count int)  {}
func (*FakeMonitor) ReportCacheWrites(count int) {}
func (*FakeMonitor) ReportPruned(count int)      {}
