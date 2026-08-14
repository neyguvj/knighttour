package monitoring

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"knighttour/types"
)

type Monitor interface {
	AddTasks(tasks ...types.Subtask)
	Start(ctx context.Context)
	Finish()
	RecordTaskCompletion(task types.Subtask, result types.Result)
}

type RealMonitor struct {
	startTime          time.Time
	totalTasks         atomic.Uint64
	completed          atomic.Uint64
	totalPaths         atomic.Uint64
	connectivityPruned atomic.Uint64
}

func NewMonitor() *RealMonitor {
	return &RealMonitor{}
}

func (m *RealMonitor) AddTasks(tasks ...types.Subtask) {
	m.totalTasks.Add(uint64(len(tasks)))
}

func (m *RealMonitor) Start(ctx context.Context) {
	m.startTime = time.Now()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
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

	if totalTasks == 0 {
		totalTasks = 1
	}

	pct := float64(completed) / float64(totalTasks) * 100

	fmt.Printf(
		"\r[%s] Tasks: %d/%d (%.1f%%) | Total paths %d | Pruned: %d",
		elapsed.String(),
		completed, totalTasks,
		pct,
		m.totalPaths.Load(),
		m.connectivityPruned.Load(),
	)
}

func (m *RealMonitor) Finish() {
	m.report()
	fmt.Printf("\n=== Final ===\n")
	fmt.Printf("Time: %s\n", time.Since(m.startTime))
	fmt.Printf("Tasks completed: %d/%d\n", m.completed.Load(), m.totalTasks.Load())
	fmt.Printf("Total paths: %d\n", m.totalPaths.Load())
	fmt.Printf("Connectivity pruned: %d\n", m.connectivityPruned.Load())
}

func (m *RealMonitor) RecordTaskCompletion(task types.Subtask, result types.Result) {
	m.completed.Add(1)
	m.totalPaths.Add(uint64(result.TotalPathsFound) * uint64(task.SymmetriesCount))
	m.connectivityPruned.Add(uint64(result.Pruned))
}

type FakeMonitor struct{}

func NewFakeMonitor() *FakeMonitor {
	return &FakeMonitor{}
}

func (f *FakeMonitor) AddTasks(tasks ...types.Subtask)                              {}
func (f *FakeMonitor) Start(ctx context.Context)                                    {}
func (f *FakeMonitor) Finish()                                                      {}
func (f *FakeMonitor) AddResult(result types.Result)                                {}
func (f *FakeMonitor) RecordTaskCompletion(task types.Subtask, result types.Result) {}
