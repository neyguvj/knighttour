package monitoring

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReportsWithoutPhaseAreNoop(t *testing.T) {
	m := NewMonitor()

	assert.NotPanics(t, func() {
		m.AddTasks(10)
		m.ReportTaskCompleted()
		m.ReportPathsFound(5)
		m.ReportCacheWrites(3)
		m.ReportPruned(2)
	})
	assert.Empty(t, m.phases)
}

func TestPhaseAccumulation(t *testing.T) {
	m := NewMonitor()

	m.BeginPhase("generation")
	m.AddTasks(6)
	m.ReportTaskCompleted()
	m.ReportCacheWrites(100)
	m.ReportPruned(42)

	m.BeginPhase("counting")
	m.AddTasks(10)
	for range 3 {
		m.ReportTaskCompleted()
	}
	m.ReportPathsFound(999)
	m.ReportPruned(7)

	assert.Len(t, m.phases, 2)

	gen := m.activePhaseAt(t, 0)
	assert.Equal(t, "generation", gen.name)
	assert.Equal(t, uint64(6), gen.tasks.Load())
	assert.Equal(t, uint64(1), gen.completed.Load())
	assert.Equal(t, uint64(0), gen.pathsFound.Load())
	assert.Equal(t, uint64(100), gen.cacheWrites.Load())
	assert.Equal(t, uint64(42), gen.pruned.Load())

	cnt := m.activePhaseAt(t, 1)
	assert.Equal(t, "counting", cnt.name)
	assert.Equal(t, uint64(10), cnt.tasks.Load())
	assert.Equal(t, uint64(3), cnt.completed.Load())
	assert.Equal(t, uint64(999), cnt.pathsFound.Load())
	assert.Equal(t, uint64(0), cnt.cacheWrites.Load())
	assert.Equal(t, uint64(7), cnt.pruned.Load())

	// BeginPhase must close the previous phase.
	assert.False(t, gen.endTime.IsZero(), "previous phase end time is recorded")
}

func TestConcurrentReportsRace(t *testing.T) {
	m := NewMonitor()
	m.BeginPhase("counting")

	const goroutines, reports = 8, 1000
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range reports {
				m.ReportPathsFound(1)
				m.ReportCacheWrites(2)
				m.ReportPruned(3)
				m.ReportTaskCompleted()
			}
		})
	}
	wg.Wait()

	ph := m.active.Load()
	assert.Equal(t, uint64(goroutines*reports), ph.pathsFound.Load())
	assert.Equal(t, uint64(2*goroutines*reports), ph.cacheWrites.Load())
	assert.Equal(t, uint64(3*goroutines*reports), ph.pruned.Load())
	assert.Equal(t, uint64(goroutines*reports), ph.completed.Load())
}

func TestFinishWithoutStartDoesNotPanic(t *testing.T) {
	m := NewMonitor()
	assert.NotPanics(t, m.Finish)
}

func TestFakeMonitorSatisfiesInterface(t *testing.T) {
	var m Monitor = NewFakeMonitor()
	m.Start(context.Background())
	m.BeginPhase("any")
	m.AddTasks(1)
	m.ReportTaskCompleted()
	m.ReportPathsFound(1)
	m.ReportCacheWrites(1)
	m.ReportPruned(1)
	m.Finish()
}

func (m *RealMonitor) activePhaseAt(t *testing.T, idx int) *phaseStats {
	t.Helper()
	m.phasesMu.Lock()
	defer m.phasesMu.Unlock()
	assert.Less(t, idx, len(m.phases))
	return m.phases[idx]
}
