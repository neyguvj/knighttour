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
			workers := runtime.NumCPU()

			for depth := 1; depth <= size*size/2; depth++ {
				b.Run("depth"+strconv.Itoa(depth), func(b *testing.B) {
					for b.Loop() {
						c.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), workers, depth, 0)
					}
				})
			}
		})
	}
}
