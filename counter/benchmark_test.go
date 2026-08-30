package counter

import (
	"context"
	"runtime"
	"strconv"
	"testing"

	"knighttour/graph"
	"knighttour/monitoring"
)

func BenchmarkCountAllToursParallel(b *testing.B) {
	for _, size := range []int{5, 6} {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			g := graph.New(size)
			c := NewCounter(g)

			for b.Loop() {
				c.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), runtime.NumCPU())
			}
		})
	}
}
