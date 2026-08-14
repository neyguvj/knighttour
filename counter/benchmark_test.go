package counter

import (
	"context"
	"testing"

	"knighttour/graph"
	"knighttour/monitoring"
)

func BenchmarkCountAllToursParallel(b *testing.B) {
	g := graph.New(5)
	c := NewCounter(g)

	for b.Loop() {
		c.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 2)
	}
}
