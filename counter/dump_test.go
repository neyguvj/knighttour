package counter

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/state"
)

// TestDumpZeroShapes runs the class pipeline and dumps every final-pass shape
// task whose h is zero for all queried ends, feeding the plan-02 stage-0
// classifier (shapecount.TestShapeFeasibilityClassification). Format: one
// "<shape-hex> <endmask-hex>" line per zero shape.
//
// Gated by env: SHAPE_DUMP=<output file> (required), SHAPE_DUMP_SIZE and
// SHAPE_DUMP_DEPTH (required), SHAPE_DUMP_SAMPLE=K keeps every K-th zero
// shape deterministically up to the sample counter (default 1 = all).
func TestDumpZeroShapes(t *testing.T) {
	out := os.Getenv("SHAPE_DUMP")
	if out == "" {
		t.Skip("set SHAPE_DUMP=<file> to produce a zero-shape dump")
	}
	size, err := strconv.Atoi(os.Getenv("SHAPE_DUMP_SIZE"))
	require.NoError(t, err, "SHAPE_DUMP_SIZE is required")
	depth, err := strconv.Atoi(os.Getenv("SHAPE_DUMP_DEPTH"))
	require.NoError(t, err, "SHAPE_DUMP_DEPTH is required")
	sample := 1
	if v := os.Getenv("SHAPE_DUMP_SAMPLE"); v != "" {
		sample, err = strconv.Atoi(v)
		require.NoError(t, err, "SHAPE_DUMP_SAMPLE must be an integer")
		require.Positive(t, sample)
	}

	f, err := os.Create(out)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	w := bufio.NewWriterSize(f, 1<<20)

	var (
		mu   sync.Mutex
		seen int64
	)
	c := NewCounter(graph.New(size))
	c.SetShapeDump(func(shape state.State, ends []int, allZero bool) {
		if !allZero {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if seen%int64(sample) != 0 {
			seen++
			return
		}
		seen++
		var endMask state.State
		for _, e := range ends {
			endMask = endMask.Visit(e)
		}
		_, _ = fmt.Fprintf(w, "%x %x\n", uint64(shape), uint64(endMask))
	})

	fm := monitoring.NewFakeMonitor()
	total := c.ParallelCountWithDepth(context.Background(), fm, runtime.NumCPU(), depth)
	if want, ok := toursExpected[size]; ok {
		require.Equal(t, want, total, "dump run must reproduce the known tour count")
	}

	require.NoError(t, w.Flush())
	classes, shapes, zeros := fm.ShapeStats()
	t.Logf("size=%d depth=%d: classes=%d shapes=%d zeros=%d dumped=%d → %s",
		size, depth, classes, shapes, zeros, seen, out)
}
