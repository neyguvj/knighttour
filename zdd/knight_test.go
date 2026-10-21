package zdd

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/graph"
)

// knightEdges lists the undirected edges of the n×n knight graph with vertex
// numbering pos = row*n + col, reusing the canonical adjacency of package
// graph. Each edge is emitted once (from its smaller endpoint). It takes
// testing.TB so tests and benchmarks share one board constructor.
func knightEdges(t testing.TB, size int) (int, [][2]int) {
	t.Helper()
	g := graph.New(size)
	var edges [][2]int
	for pos := range g.GetTotalCells() {
		for _, nb := range g.GetNeighbors(pos) {
			if nb > pos {
				edges = append(edges, [2]int{pos, nb})
			}
		}
	}
	return g.GetTotalCells(), edges
}

// directedTours converts a family count (edge sets) to the directed-tour
// convention of docs/requirements.md: each tour has two traversal directions.
func directedTours(sets uint64) uint64 { return 2 * sets }

// TestKnightBoardPaths pins the ZDD path count against the reference numbers
// of docs/requirements.md (directed open tours). The 5×5 board is the main
// control of this increment: 864 path sets × 2 = 1728 directed tours.
func TestKnightBoardPaths(t *testing.T) {
	cases := []struct {
		size         int
		wantSets     uint64 // Hamiltonian-path edge sets
		wantDirected uint64 // docs/requirements.md "Открытых туров"
	}{
		{size: 3, wantSets: 0, wantDirected: 0},      // isolated center vertex
		{size: 4, wantSets: 0, wantDirected: 0},      // no knight's tour exists
		{size: 5, wantSets: 864, wantDirected: 1728}, // reference docs/requirements.md
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("size%d", tc.size), func(t *testing.T) {
			n, edges := knightEdges(t, tc.size)
			d, err := Build(n, edges, Paths, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.wantSets, d.Count(), "path sets")
			assert.Equal(t, tc.wantDirected, directedTours(d.Count()), "directed tours vs requirements")
			assert.Positive(t, d.LiveNodes())
		})
	}
}

// TestKnightBoardCycles pins the cycle family against docs/requirements.md
// (directed closed tours ×2 convention): 6×6 — 9 862 sets = 19 724 directed;
// odd boards have no closed knight's tour by the bipartite parity criterion
// and must come to zero (the shortcut or the build, result identical).
func TestKnightBoardCycles(t *testing.T) {
	cases := []struct {
		size         int
		wantSets     uint64
		wantDirected uint64
	}{
		{size: 5, wantSets: 0, wantDirected: 0},        // odd board: parity
		{size: 6, wantSets: 9862, wantDirected: 19724}, // reference docs/requirements.md
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("size%d", tc.size), func(t *testing.T) {
			n, edges := knightEdges(t, tc.size)
			d, err := Build(n, edges, Cycles, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.wantSets, d.Count(), "cycle sets")
			assert.Equal(t, tc.wantDirected, directedTours(d.Count()), "directed tours vs requirements")
		})
	}
}

// TestKnight7x7CyclesZero pins the odd-board control of the spec unit budget:
// 7×7 cycle family is empty (Schwenk's criterion). Separated from the table
// so its runtime profile stays visible.
func TestKnight7x7CyclesZero(t *testing.T) {
	n, edges := knightEdges(t, 7)
	d, err := Build(n, edges, Cycles, nil)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), d.Count(), "odd board has no closed knight's tour")
}

// TestKnight5x5Enumeration cross-checks the main control end to end: the
// family enumerated by All() has exactly 864 sets, every one of them a valid
// Hamiltonian path of the 5×5 knight graph with no duplicates.
func TestKnight5x5Enumeration(t *testing.T) {
	n, edges := knightEdges(t, 5)
	d, err := Build(n, edges, Paths, nil)
	require.NoError(t, err)

	seen := make(map[string]bool, 864)
	count := 0
	for set := range d.All() {
		require.True(t, isHamiltonianPath(n, edges, set), "invalid path set at enumeration index %d: %v", count, set)
		key := fmt.Sprint(set)
		require.False(t, seen[key], "duplicate set in All(): %v", set)
		seen[key] = true
		count++
	}
	assert.Equal(t, 864, count, "enumerated sets")
	assert.Equal(t, uint64(count), d.Count(), "All() agrees with Count()")
}

// TestKnight5x5Determinism builds the 5×5 diagram twice and requires identical
// node counts and weights (specs/zdd.md «Детерминизм»).
func TestKnight5x5Determinism(t *testing.T) {
	n, edges := knightEdges(t, 5)
	d1, err := Build(n, edges, Paths, nil)
	require.NoError(t, err)
	d2, err := Build(n, edges, Paths, nil)
	require.NoError(t, err)
	assert.Equal(t, d1.LiveNodes(), d2.LiveNodes())
	assert.Equal(t, d1.LevelPeak(), d2.LevelPeak())
	assert.Equal(t, d1.Count(), d2.Count())
	assert.LessOrEqual(t, d1.LevelPeak(), d1.LiveNodes(), "level peak is a slice of the arena")
	assert.Positive(t, d1.LevelPeak())
}
