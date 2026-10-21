package zdd

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrderNarrowIsPermutation checks the return contract on assorted graphs:
// OrderNarrow always yields a permutation of [0, m).
func TestOrderNarrowIsPermutation(t *testing.T) {
	cases := []struct {
		name  string
		edges [][2]int
		n     int
	}{
		{name: "empty", n: 3, edges: nil},
		{name: "single edge", n: 2, edges: [][2]int{{0, 1}}},
		{name: "C_6", n: 6, edges: cycleGraph(6)},
		{name: "K_5", n: 5, edges: completeGraph(5)},
		{name: "Petersen", n: 10, edges: petersenEdges()},
		{name: "knight 5x5", n: 25, edges: knightEdgeList(t, 5)},
		// Regression: a corridor on high vertex ids used to feed a neighbour
		// id into the edge-index space and panic in the greedy seed.
		{name: "corridor on high ids", n: 10, edges: [][2]int{{9, 8}, {8, 7}, {7, 6}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OrderNarrow(tc.n, tc.edges)
			assert.Len(t, got, len(tc.edges))
			sorted := slices.Clone(got)
			slices.Sort(sorted)
			assert.Equal(t, rangePermutation(len(tc.edges)), sorted)
		})
	}
}

// rangePermutation returns [0, m), the row order.
func rangePermutation(m int) []int {
	ord := make([]int, m)
	for i := range ord {
		ord[i] = i
	}
	return ord
}

// TestOrderNarrowDeterministic calls OrderNarrow twice per input and requires
// elementwise equality — the order must not depend on process state.
func TestOrderNarrowDeterministic(t *testing.T) {
	cases := []struct {
		name  string
		edges [][2]int
		n     int
	}{
		{name: "K_6", n: 6, edges: completeGraph(6)},
		{name: "Petersen", n: 10, edges: petersenEdges()},
		{name: "knight 6x6", n: 36, edges: knightEdgeList(t, 6)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, slices.Equal(OrderNarrow(tc.n, tc.edges), OrderNarrow(tc.n, tc.edges)))
		})
	}
}

// TestOrderNarrowWidthNoWorseThanRow pins the selection property: the measured
// cut width of the narrow order never exceeds that of the row order.
func TestOrderNarrowWidthNoWorseThanRow(t *testing.T) {
	cases := []struct {
		name  string
		edges [][2]int
		n     int
	}{
		{name: "C_6", n: 6, edges: cycleGraph(6)},
		{name: "K_5", n: 5, edges: completeGraph(5)},
		{name: "Petersen", n: 10, edges: petersenEdges()},
		{name: "knight 5x5", n: 25, edges: knightEdgeList(t, 5)},
		{name: "knight 6x6", n: 36, edges: knightEdgeList(t, 6)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			narrow := OrderNarrow(tc.n, tc.edges)
			wRow := cutWidth(tc.n, tc.edges, rangePermutation(len(tc.edges)))
			wNarrow := cutWidth(tc.n, tc.edges, narrow)
			t.Logf("cut width: row=%d narrow=%d", wRow, wNarrow)
			assert.LessOrEqual(t, wNarrow, wRow, "narrow order must not be wider than row order")
		})
	}
}

// TestOrderNarrowSameFamily checks the order-invariance of the represented
// family: building with OrderNarrow yields exactly the sets built in row
// order (specs/zdd.md — order selects variables only, never membership).
func TestOrderNarrowSameFamily(t *testing.T) {
	cases := []struct {
		name  string
		edges [][2]int
		fam   Family
		n     int
	}{
		{name: "K_5 paths", n: 5, edges: completeGraph(5)},
		{fam: Cycles, name: "K_5 cycles", n: 5, edges: completeGraph(5)},
		{fam: Cycles, name: "knight 5x5 cycles", n: 25, edges: knightEdgeList(t, 5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dRow, err := Build(tc.n, tc.edges, tc.fam, nil)
			require.NoError(t, err)
			dNarrow, err := Build(tc.n, tc.edges, tc.fam, OrderNarrow(tc.n, tc.edges))
			require.NoError(t, err)

			assert.Equal(t, dRow.Count(), dNarrow.Count())
			var rowSets, narrowSets [][]int
			for set := range dRow.All() {
				rowSets = append(rowSets, set)
			}
			for set := range dNarrow.All() {
				narrowSets = append(narrowSets, set)
			}
			assert.ElementsMatch(t, rowSets, narrowSets)
		})
	}
}

// TestOrderNarrowInvalidOrderRejectedByBuild: OrderNarrow itself validates
// nothing, but the result must still pass Build's permutation check.
func TestOrderNarrowInvalidOrderRejectedByBuild(t *testing.T) {
	ord := OrderNarrow(3, [][2]int{{0, 1}, {1, 2}})
	require.Len(t, ord, 2)
	truncated := ord[:1]
	_, err := Build(3, [][2]int{{0, 1}, {1, 2}}, Paths, truncated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "order length")
}

// knightEdgeList returns only the edge list of the size×size knight graph
// (the vertex count is known from the board).
func knightEdgeList(t *testing.T, size int) [][2]int {
	t.Helper()
	_, edges := knightEdges(t, size)
	return edges
}
