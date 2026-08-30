package counter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/graph"
	"knighttour/monitoring"
)

func TestSequentalCount(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 1)

	assert.Equal(t, uint64(1728), count, "Expected %d count for 5x5 board, got %d", 1728, count)
}

func TestParallelCount(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 8)

	assert.Equal(t, uint64(1728), count, "Expected %d count for 5x5 board, got %d", 1728, count)
}

func TestParallelCountWithDepth(t *testing.T) {
	size := 5
	g := graph.New(size)
	counter := NewCounter(g)

	for depth := range size * size {
		count := counter.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), 8, depth)
		assert.Equal(t, uint64(1728), count, "Expected %d count for 5x5 board, got %d", 1728, count)
	}
}

func TestCounterFromPosition(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.CountFromPosition(context.Background(), 0)

	assert.Positive(t, count, "Should find paths from valid starting position")
}
