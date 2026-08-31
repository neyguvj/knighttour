package monitoring

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestEstimateRemaining(t *testing.T) {
	tests := []struct {
		name      string
		elapsed   time.Duration
		completed uint64
		total     uint64
		want      time.Duration
		wantOK    bool
	}{
		{name: "no completed tasks is infinite", elapsed: 5 * time.Second, completed: 0, total: 10},
		{name: "unknown total is infinite", elapsed: 5 * time.Second, completed: 3, total: 0},
		{name: "nothing at all is infinite", elapsed: time.Second},
		{
			name:    "phase fully done is zero",
			elapsed: 1234 * time.Millisecond, completed: 8, total: 8,
			want: 0, wantOK: true,
		},
		{
			name:    "linear extrapolation quarter done",
			elapsed: 10 * time.Second, completed: 2, total: 8,
			want: 30 * time.Second, wantOK: true,
		},
		{
			name:    "linear extrapolation seven of eight",
			elapsed: 70 * time.Second, completed: 7, total: 8,
			want: 10 * time.Second, wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := estimateRemaining(tc.elapsed, tc.completed, tc.total)
			assert.Equal(t, tc.wantOK, ok)
			if ok {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestFmtDurMillisecondPrecision(t *testing.T) {
	assert.Equal(t, "1.235s", fmtDur(1234567*time.Microsecond))
	assert.Equal(t, "0s", fmtDur(0))
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)

	os.Stdout = w
	fn()
	require.NoError(t, w.Close())
	os.Stdout = old

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func TestReportFormatWithETA(t *testing.T) {
	m := NewMonitor()
	m.startTime = time.Now()
	m.BeginPhase("counting")
	ph := m.active.Load()
	ph.startTime = time.Now().Add(-10 * time.Second)

	out := captureStdout(t, m.report)
	assert.True(t, strings.HasPrefix(out, clearLine), "report must start with ANSI clear-line")
	assert.Contains(t, out, "| ETA --", "no completed tasks yet -> unknown ETA")

	m.AddTasks(8)
	for range 4 {
		m.ReportTaskCompleted()
	}
	out = captureStdout(t, m.report)
	assert.Regexp(t, `\| ETA \d+(\.\d+)?s$`, out)

	for range 4 {
		m.ReportTaskCompleted()
	}
	out = captureStdout(t, m.report)
	assert.Contains(t, out, "| ETA 0s", "finished phase -> zero ETA")
}

func (m *RealMonitor) activePhaseAt(t *testing.T, idx int) *phaseStats {
	t.Helper()
	m.phasesMu.Lock()
	defer m.phasesMu.Unlock()
	assert.Less(t, idx, len(m.phases))
	return m.phases[idx]
}
