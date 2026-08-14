package counter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/graph"
	"knighttour/monitoring"
)

func TestCounterCountAllTours(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 1)

	assert.NotZero(t, count, "Expected positive count for 5x5 board")
}

func TestCounterParallel(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	countSeq := counter.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 1)
	countPar := counter.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 4)

	assert.Equal(t, countSeq, countPar, "Sequential and parallel counts should match")
}

func TestCounterFromPosition(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.CountFromPosition(context.Background(), 0)

	assert.Greater(t, count, 0, "Should find paths from valid starting position")
}
