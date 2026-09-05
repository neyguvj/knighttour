package counter

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"testing"

	"knighttour/graph"
	"knighttour/monitoring"
)

// oracleBenchCase is one BenchmarkCountAllToursOracle configuration.
// oracleDepth == 0 means the legacy prefix-cache reversal (anchor for paired
// comparison inside the same run).
type oracleBenchCase struct {
	size        int
	root        int
	oracleDepth int
}

// fullOracleSweep enumerates the development sweep: roots {4..8, 10, 12}
// crossed with even oracle depths from 2 up to min(total/2, total-root).
// Depths beyond half the board are never used in practice — computeH costs
// O(D·2^D) per class while an early stop saves little (specs/oracle.md caps
// defaults at ~16), so the sweep does not go there. Runs via
// ORACLE_FULL_SWEEP=1 / `make bench-oracle-full`; prune its matrix down to
// oracleBenchCases' steady-state set (specs/counter.md).
func fullOracleSweep() []oracleBenchCase {
	var cases []oracleBenchCase
	for _, size := range []int{5, 6} {
		total := size * size
		for _, root := range []int{4, 5, 6, 7, 8, 10, 12} {
			cases = append(cases, oracleBenchCase{size, root, 0})
			maxD := min(total/2, total-root)
			for d := 2; d <= maxD; d += 2 {
				cases = append(cases, oracleBenchCase{size, root, d})
			}
			if maxD%2 == 1 {
				cases = append(cases, oracleBenchCase{size, root, maxD})
			}
		}
	}
	return cases
}

// oracleBenchCases returns the benchmark table: curve edges plus the fastest
// configs per size — regression sentinels kept cheap enough for `make bench`
// (the deep-tail point dominates its runtime). ORACLE_FULL_SWEEP=1 swaps in
// the full development sweep; retune with:
//
//	make bench-oracle-full
func oracleBenchCases() []oracleBenchCase {
	if os.Getenv("ORACLE_FULL_SWEEP") != "" {
		return fullOracleSweep()
	}
	// Measured 2026-09, M4 Max (full sweep in specs/counter.md): deeper roots
	// are monotonically faster up to 12; oracle adds ~+15% over legacy at the
	// same root for D≤8 and ×2 at D=10. Sentinels: fastest configs, the
	// compute-cost onset, and the top edge (half-board cap).
	return []oracleBenchCase{
		{5, 8, 0},   // legacy anchor / fastest on 5×5
		{5, 8, 4},   // cheapest oracle mode
		{5, 12, 10}, // compute-cost onset at deep root
		{5, 12, 12}, // top edge (total/2 cap)
		{6, 12, 0},  // legacy anchor / fastest overall
		{6, 12, 4},  // fastest oracle point on 6×6
		{6, 12, 10}, // compute-cost onset: ~2.5x of D8 — sensitive canary
		{6, 12, 18}, // top edge (total/2 cap): dominates this bench's runtime
	}
}

func BenchmarkCountAllToursOracle(b *testing.B) {
	workers := runtime.NumCPU()
	for _, tc := range oracleBenchCases() {
		mode := "oracleD" + strconv.Itoa(tc.oracleDepth)
		if tc.oracleDepth == 0 {
			mode = "legacy"
		}
		name := "size" + strconv.Itoa(tc.size) +
			"/root" + strconv.Itoa(tc.root) + "/" + mode
		b.Run(name, func(b *testing.B) {
			c := NewCounter(graph.New(tc.size))
			for b.Loop() {
				c.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), workers, tc.root, tc.oracleDepth)
			}
		})
	}
}

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
