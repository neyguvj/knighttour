package monitoring

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

type Monitor interface {
	Start(ctx context.Context)
	Finish()
	AddTasks(count int)
	ReportTaskCompleted()
	ReportPathsFound(count int)
	ReportPathsCached(count int)
}

type RealMonitor struct {
	startTime   time.Time
	started     atomic.Bool
	totalTasks  atomic.Uint64
	completed   atomic.Uint64
	totalPaths  atomic.Uint64
	cachedPaths atomic.Uint64
}

func NewMonitor() *RealMonitor {
	return &RealMonitor{}
}

func (m *RealMonitor) AddTasks(count int) {
	m.totalTasks.Add(uint64(count))
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
				m.report()
				return
			}
		}
	}()
}

func (m *RealMonitor) report() {
	elapsed := time.Since(m.startTime)
	completed := m.completed.Load()
	totalTasks := m.totalTasks.Load()

	pct := float64(completed) / float64(totalTasks) * 100

	fmt.Printf(
		"\r[%s] Tasks: %d/%d (%.1f%%) | Total paths %d | Cached paths: %d",
		elapsed.String(),
		completed, totalTasks,
		pct,
		m.totalPaths.Load(),
		m.cachedPaths.Load(),
	)
}

func (m *RealMonitor) Finish() {
	if !m.started.Load() {
		return
	}
	m.report()
	m.started.Store(false)
	fmt.Printf("\n=== Final ===\n")
	fmt.Printf("Time: %s\n", time.Since(m.startTime))
	fmt.Printf("Tasks completed: %d/%d\n", m.completed.Load(), m.totalTasks.Load())
	fmt.Printf("Total paths: %d\n", m.totalPaths.Load())
	fmt.Printf("Cached paths: %d\n", m.cachedPaths.Load())
}

func (m *RealMonitor) ReportTaskCompleted() {
	m.completed.Add(1)
}

func (m *RealMonitor) ReportPathsFound(count int) {
	m.totalPaths.Add(uint64(count))
}

func (m *RealMonitor) ReportPathsCached(count int) {
	m.cachedPaths.Add(uint64(count))
}

type FakeMonitor struct{}

func NewFakeMonitor() *FakeMonitor {
	return &FakeMonitor{}
}

func (*FakeMonitor) Start(ctx context.Context)   {}
func (*FakeMonitor) Finish()                     {}
func (*FakeMonitor) AddTasks(count int)          {}
func (*FakeMonitor) ReportTaskCompleted()        {}
func (*FakeMonitor) ReportPathsFound(count int)  {}
func (*FakeMonitor) ReportPathsCached(count int) {}
