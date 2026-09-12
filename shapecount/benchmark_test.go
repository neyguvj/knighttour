package shapecount_test

import (
	"math/rand"
	"testing"

	"knighttour/graph"
	"knighttour/pruner"
	"knighttour/shapecount"
	"knighttour/state"
	"knighttour/types"
)

// BenchmarkCountShapeL2 is the plan-04 stage-0 instrument: a fixed deterministic
// sample of shapes (random walks on 6×6, sizes 14..18) counted with each L2
// check independently switched on/off. Extra metrics expose the evaluated DP
// state count and per-reason cuts, so "nodes cut" and "cost per state" are
// read side by side; ns/op is the wall-clock verdict.
func BenchmarkCountShapeL2(b *testing.B) {
	const size = 6
	g := graph.New(size)

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic bench sample
	type shapeCase struct {
		ends []int
		mask state.State
	}
	var shapes []shapeCase
	for len(shapes) < 24 {
		mask, ok := randomWalkMask(rng, g, 14+rng.Intn(5)) // sizes 14..18
		if !ok {
			continue
		}
		var ends []int
		for e := range mask.AllVisited() {
			ends = append(ends, e)
		}
		shapes = append(shapes, shapeCase{mask: mask, ends: ends})
	}

	levels := []struct {
		name string
		mask pruner.L2Checks
	}{
		{"off", pruner.L2None},
		{"artic", pruner.L2Articulation},
		{"chain", pruner.L2ForcedChain},
		{"all", pruner.L2All},
	}
	for _, lv := range levels {
		b.Run(lv.name, func(b *testing.B) {
			sc := shapecount.New(g)
			sc.SetL2(lv.mask, pruner.DefaultMinL2)

			var agg types.Result
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var res types.Result
				for _, s := range shapes {
					sc.CountShape(s.mask, s.ends, &res)
				}
				agg = res
			}
			b.StopTimer()

			b.ReportMetric(float64(agg.DPStates), "dpstates/op")
			b.ReportMetric(float64(agg.Pruned), "pruned/op")
			b.ReportMetric(float64(agg.PrunedArticulation), "prunedArtic/op")
			b.ReportMetric(float64(agg.PrunedForcedChain), "prunedChain/op")
		})
	}
}
