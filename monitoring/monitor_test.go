package monitoring

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"knighttour/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportsWithoutPhaseAreNoop(t *testing.T) {
	m := NewMonitor()

	assert.NotPanics(t, func() {
		m.AddTasks(10)
		m.ReportTaskCompleted()
		m.ReportPathsFound(5)
		m.ReportSubtask(&types.Result{CacheWrites: 3, Pruned: 2})
	})
	assert.Empty(t, m.phases)
}

func TestPhaseAccumulation(t *testing.T) {
	m := NewMonitor()

	m.BeginPhase("generation")
	m.AddTasks(6)
	m.ReportTaskCompleted()
	m.ReportSubtask(&types.Result{CacheWrites: 100, Pruned: 42, PrunedDeadEnd: 40, PrunedEndpoints: 2})

	m.BeginPhase("counting")
	m.AddTasks(10)
	for range 3 {
		m.ReportTaskCompleted()
	}
	m.ReportPathsFound(999)
	m.ReportSubtask(&types.Result{Pruned: 7, PrunedDisconn: 7})

	assert.Len(t, m.phases, 2)

	gen := m.activePhaseAt(t, 0)
	assert.Equal(t, "generation", gen.name)
	assert.Equal(t, uint64(6), gen.tasks.Load())
	assert.Equal(t, uint64(1), gen.completed.Load())
	assert.Equal(t, uint64(0), gen.pathsFound.Load())
	assert.Equal(t, uint64(100), gen.cacheWrites.Load())
	assert.Equal(t, uint64(42), gen.prunedTotal())
	assert.Equal(t, uint64(40), gen.prunedDeadEnd.Load())
	assert.Equal(t, uint64(2), gen.prunedEndpoints.Load())

	cnt := m.activePhaseAt(t, 1)
	assert.Equal(t, "counting", cnt.name)
	assert.Equal(t, uint64(10), cnt.tasks.Load())
	assert.Equal(t, uint64(3), cnt.completed.Load())
	assert.Equal(t, uint64(999), cnt.pathsFound.Load())
	assert.Equal(t, uint64(0), cnt.cacheWrites.Load())
	assert.Equal(t, uint64(7), cnt.prunedTotal())

	// BeginPhase must close the previous phase.
	assert.False(t, gen.endTime.IsZero(), "previous phase end time is recorded")
}

func TestReportSubtaskSumsByReason(t *testing.T) {
	m := NewMonitor()
	m.BeginPhase("counting")

	m.ReportSubtask(&types.Result{PrunedDeadEnd: 3, PrunedNoCont: 1})
	m.ReportSubtask(&types.Result{PrunedDisconn: 4, PrunedEndpoints: 2})
	m.ReportSubtask(&types.Result{PrunedForcedChain: 6})

	ph := m.active.Load()
	assert.Equal(t, uint64(3), ph.prunedDeadEnd.Load())
	assert.Equal(t, uint64(1), ph.prunedNoCont.Load())
	assert.Equal(t, uint64(4), ph.prunedDisconn.Load())
	assert.Equal(t, uint64(2), ph.prunedEndpoints.Load())
	assert.Equal(t, uint64(6), ph.prunedForcedChain.Load())
	assert.Equal(t, uint64(16), ph.prunedTotal())
	assert.Equal(t, uint64(16), m.Phase("counting").Pruned)
}

// Reversal task-cache lookups fold through ReportSubtask into the phase
// snapshot (specs/monitoring.md).
func TestReportSubtaskFoldsCacheHitsMisses(t *testing.T) {
	m := NewMonitor()
	m.BeginPhase("counting")

	m.ReportSubtask(&types.Result{CacheHits: 7, CacheMisses: 2})
	m.ReportSubtask(&types.Result{CacheHits: 1})

	ph := m.Phase("counting")
	assert.Equal(t, uint64(8), ph.CacheHits)
	assert.Equal(t, uint64(2), ph.CacheMisses)
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
				m.ReportSubtask(&types.Result{CacheWrites: 2, PrunedDeadEnd: 3})
				m.ReportTaskCompleted()
			}
		})
	}
	wg.Wait()

	ph := m.active.Load()
	assert.Equal(t, uint64(goroutines*reports), ph.pathsFound.Load())
	assert.Equal(t, uint64(2*goroutines*reports), ph.cacheWrites.Load())
	assert.Equal(t, uint64(3*goroutines*reports), ph.prunedTotal())
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
	m.ReportSubtask(&types.Result{CacheWrites: 1, PrunedDeadEnd: 1})
	m.Finish()
}

func TestFakeMonitorRecordsPerPhase(t *testing.T) {
	tests := []struct {
		reports func(m *FakeMonitor)
		check   func(t *testing.T, m *FakeMonitor)
		name    string
	}{
		{
			name: "no phase ignores reports",
			reports: func(m *FakeMonitor) {
				m.AddTasks(10)
				m.ReportSubtask(&types.Result{CacheWrites: 5})
			},
			check: func(t *testing.T, m *FakeMonitor) {
				assert.Equal(t, PhaseStats{}, m.Totals())
			},
		},
		{
			name: "phases are tracked separately",
			reports: func(m *FakeMonitor) {
				m.BeginPhase("gen")
				m.AddTasks(4)
				m.ReportSubtask(&types.Result{CacheWrites: 7})
				m.BeginPhase("counting")
				m.AddTasks(100)
				m.ReportTaskCompleted()
				m.ReportPathsFound(42)
			},
			check: func(t *testing.T, m *FakeMonitor) {
				gen := m.Phase("gen")
				assert.Equal(t, uint64(4), gen.Tasks)
				assert.Equal(t, uint64(1), gen.Subtasks)
				assert.Equal(t, uint64(7), gen.CacheWrites)

				cnt := m.Phase("counting")
				assert.Equal(t, uint64(100), cnt.Tasks)
				assert.Equal(t, uint64(1), cnt.Completed)
				assert.Equal(t, uint64(42), cnt.PathsFound)

				tot := m.Totals()
				assert.Equal(t, uint64(104), tot.Tasks)
				assert.Equal(t, uint64(7), tot.CacheWrites)
			},
		},
		{
			name: "pruning breakdown recorded",
			reports: func(m *FakeMonitor) {
				m.BeginPhase("gen")
				m.ReportSubtask(&types.Result{PrunedDeadEnd: 3, PrunedNoCont: 1})
				m.ReportSubtask(&types.Result{PrunedDisconn: 4, PrunedEndpoints: 2})
			},
			check: func(t *testing.T, m *FakeMonitor) {
				gen := m.Phase("gen")
				assert.Equal(t, uint64(2), gen.Subtasks)
				assert.Equal(t, uint64(3), gen.PrunedDeadEnd)
				assert.Equal(t, uint64(1), gen.PrunedNoCont)
				assert.Equal(t, uint64(4), gen.PrunedDisconn)
				assert.Equal(t, uint64(2), gen.PrunedEndpoints)
				assert.Equal(t, uint64(10), gen.Pruned)
			},
		},
		{
			name: "same phase name accumulates",
			reports: func(m *FakeMonitor) {
				m.BeginPhase("gen")
				m.AddTasks(2)
				m.BeginPhase("counting")
				m.BeginPhase("gen")
				m.AddTasks(3)
			},
			check: func(t *testing.T, m *FakeMonitor) {
				assert.Equal(t, uint64(5), m.Phase("gen").Tasks)
				assert.Equal(t, uint64(0), m.Phase("counting").Tasks)
				assert.Equal(t, uint64(5), m.Totals().Tasks)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewFakeMonitor()
			tt.reports(m)
			tt.check(t, m)
		})
	}
}

func TestFakeMonitorConcurrentReports(t *testing.T) {
	const goroutines, reports = 8, 200
	m := NewFakeMonitor()
	m.BeginPhase("gen")

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range reports {
				m.AddTasks(1)
				m.ReportTaskCompleted()
				m.ReportSubtask(&types.Result{CacheWrites: 2})
			}
		})
	}
	wg.Wait()

	ph := m.Phase("gen")
	assert.Equal(t, uint64(goroutines*reports), ph.Tasks)
	assert.Equal(t, uint64(goroutines*reports), ph.Completed)
	assert.Equal(t, uint64(goroutines*reports), ph.Subtasks)
	assert.Equal(t, uint64(2*goroutines*reports), ph.CacheWrites)
}

func TestMonitorsShareCountingLogic(t *testing.T) {
	exercise := func(m Monitor) {
		m.BeginPhase("gen A")
		m.AddTasks(3)
		m.ReportTaskCompleted()
		m.ReportSubtask(&types.Result{CacheWrites: 11, PrunedDeadEnd: 4, PrunedNoCont: 2})

		m.BeginPhase("counting")
		m.AddTasks(5)
		for range 4 {
			m.ReportTaskCompleted()
		}
		m.ReportPathsFound(77)
		m.ReportSubtask(&types.Result{PrunedDisconn: 3, PrunedEndpoints: 1, PrunedForcedChain: 4, CacheHits: 9, CacheMisses: 2})
	}

	realM, fakeM := NewMonitor(), NewFakeMonitor()
	// Fake's Start only anchors the clock (no reporter goroutine), which lets
	// Finish close the last phase exactly like the real monitor does.
	fakeM.Start(context.Background())
	exercise(realM)
	exercise(fakeM)

	// Durations are wall-clock readings and differ run to run; everything else
	// must match bit for bit — that is the point of the shared core.
	assert.Equal(t, withoutDuration(realM.Totals()), withoutDuration(fakeM.Totals()))
	assert.Equal(t, withoutDuration(realM.Phase("gen A")), withoutDuration(fakeM.Phase("gen A")))
	assert.Equal(t, withoutDuration(realM.Phase("counting")), withoutDuration(fakeM.Phase("counting")))
	assert.Equal(t, realM.Phase("missing"), fakeM.Phase("missing"))

	gen := fakeM.Phase("gen A")
	assert.Equal(t, uint64(1), gen.Subtasks)
	assert.Equal(t, uint64(6), gen.Pruned)
	assert.Greater(t, gen.Duration, time.Duration(0), "BeginPhase closed the previous phase")
	assert.Zero(t, fakeM.Phase("counting").Duration, "open phase has no duration yet")

	fakeM.Finish()
	assert.Greater(t, fakeM.Phase("counting").Duration, time.Duration(0))
}

func TestFakeMonitorPrintsNothing(t *testing.T) {
	m := NewFakeMonitor()

	out := captureStdout(t, func() {
		m.Start(context.Background())
		m.BeginPhase("gen A")
		m.AddTasks(2)
		m.ReportTaskCompleted()
		m.ReportSubtask(&types.Result{CacheWrites: 5})
		m.Finish()
	})

	assert.Empty(t, out, "fake monitor emits neither live nor final reports")
	assert.Equal(t, uint64(1), m.Phase("gen A").Completed)
}

func TestRealMonitorPrintsFinalReport(t *testing.T) {
	m := NewMonitor()
	m.started.Store(true)
	m.startTime = time.Now()

	out := captureStdout(t, func() {
		m.BeginPhase("gen A")
		m.AddTasks(1)
		m.ReportTaskCompleted()
		m.Finish()
	})

	assert.Contains(t, out, "=== Final ===")
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

func TestLiveLineConditionalSegments(t *testing.T) {
	m := NewMonitor()
	m.startTime = time.Now()
	m.BeginPhase("counting")

	out := captureStdout(t, m.report)
	assert.NotContains(t, out, "Writes", "no cache writes yet -> segment hidden")
	assert.NotContains(t, out, "Hits", "no task-cache lookups yet -> segment hidden")

	m.ReportSubtask(&types.Result{CacheWrites: 42})
	out = captureStdout(t, m.report)
	assert.Contains(t, out, "| Writes 42", "generation-style phase shows writes")

	// Hits appears once lookups happened (reversal counting), printed after Writes.
	m.ReportSubtask(&types.Result{CacheHits: 5, CacheMisses: 2})
	out = captureStdout(t, m.report)
	assert.Contains(t, out, "| Hits 5/2", "hits/misses segment appears")
	assert.Less(t, strings.Index(out, "Writes"), strings.Index(out, "Hits"), "Hits follows Writes")

	// A phase with misses only still prints the segment (at least one non-zero).
	m2 := NewMonitor()
	m2.startTime = time.Now()
	m2.BeginPhase("counting")
	m2.ReportSubtask(&types.Result{CacheMisses: 3})
	out = captureStdout(t, m2.report)
	assert.Contains(t, out, "| Hits 0/3", "misses alone make the segment appear")
}

func TestFinalReportFormat(t *testing.T) {
	m := NewMonitor()
	m.startTime = time.Now()
	m.started.Store(true)

	m.BeginPhase("generation")
	m.AddTasks(2)
	m.ReportTaskCompleted()
	m.ReportSubtask(&types.Result{CacheWrites: 7, Pruned: 5, PrunedDeadEnd: 4, PrunedEndpoints: 1})

	m.BeginPhase("counting")
	m.AddTasks(1)
	m.ReportPathsFound(100)
	m.ReportSubtask(&types.Result{PrunedDisconn: 3, PrunedForcedChain: 1, CacheHits: 5, CacheMisses: 2})
	m.ReportTaskCompleted()

	out := captureStdout(t, m.Finish)

	assert.Contains(t, out, "=== Final ===")
	assert.Contains(t, out, "Phase generation [", "generation phase line present")
	assert.Contains(t, out, "writes 7", "generation writes present")
	assert.Contains(t, out, "pruned 5 (deadend 4, endpoints 1)", "breakdown lists only non-zero reasons")

	countingLine := ""
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "Phase counting ") {
			countingLine = line
		}
	}
	require.NotEmpty(t, countingLine)
	assert.NotContains(t, countingLine, "writes", "no zero writes segment in counting phase")
	assert.Contains(t, countingLine, "hits 5/2", "task-cache hits/misses appear on the phase line")
	assert.Contains(t, countingLine, "pruned 4 (disconn 3, chain 1)",
		"write-gate reasons extend the breakdown in the fixed order")
	genLine := ""
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "Phase generation ") {
			genLine = line
		}
	}
	assert.NotContains(t, genLine, "hits", "phases without lookups print no hits segment")
	assert.NotContains(t, out, "Shapes:", "the final report has no shape section (ADR-016)")
	assert.Contains(t, out, "Total paths: 100")
}

func withoutDuration(ps PhaseStats) PhaseStats {
	ps.Duration = 0
	return ps
}

func (m *RealMonitor) activePhaseAt(t *testing.T, idx int) *phaseStats {
	t.Helper()
	m.phasesMu.Lock()
	defer m.phasesMu.Unlock()
	assert.Less(t, idx, len(m.phases))
	return m.phases[idx]
}
