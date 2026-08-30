package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/state"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name  string
		size  int
		total int
	}{
		{"5x5 board", 5, 25},
		{"6x6 board", 6, 36},
		{"7x7 board", 7, 49},
		{"8x8 board", 8, 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := New(tt.size)
			assert.Equal(t, tt.size, g.Size(), "Size()")
			assert.Equal(t, tt.total, g.GetTotalCells(), "GetTotalCells()")
		})
	}
}

func TestGetNeighbors(t *testing.T) {
	g := New(5)

	tests := []struct {
		pos      int
		expected []int
	}{
		{0, []int{7, 11}},
		{1, []int{8, 10, 12}},
		{2, []int{5, 9, 11, 13}},
		{3, []int{6, 12, 14}},
		{4, []int{7, 13}},
		{5, []int{2, 12, 16}},
		{6, []int{3, 13, 15, 17}},
		{7, []int{0, 4, 10, 14, 16, 18}},
		{8, []int{1, 11, 17, 19}},
		{9, []int{2, 12, 18}},
		{10, []int{1, 7, 17, 21}},
		{11, []int{0, 2, 8, 18, 20, 22}},
		{12, []int{1, 3, 5, 9, 15, 19, 21, 23}},
		{13, []int{2, 4, 6, 16, 22, 24}},
		{14, []int{3, 7, 17, 23}},
		{15, []int{6, 12, 22}},
		{16, []int{5, 7, 13, 23}},
		{17, []int{6, 8, 10, 14, 20, 24}},
		{18, []int{7, 9, 11, 21}},
		{19, []int{8, 12, 22}},
		{20, []int{11, 17}},
		{21, []int{10, 12, 18}},
		{22, []int{11, 13, 15, 19}},
		{23, []int{12, 14, 16}},
		{24, []int{13, 17}},
	}

	for _, tt := range tests {
		t.Run("pos_"+string(rune('0'+tt.pos)), func(t *testing.T) {
			neighbors := g.GetNeighbors(tt.pos)
			assert.Equal(t, len(tt.expected), len(neighbors), "GetNeighbors(%d) length", tt.pos)

			for i, n := range neighbors {
				assert.Equal(t, tt.expected[i], n, "GetNeighbors(%d)[%d]", tt.pos, i)
			}
		})
	}
}

func TestGetDegree(t *testing.T) {
	g := New(5)

	tests := []struct {
		pos      int
		expected int
	}{
		{0, 2}, {1, 3}, {2, 4}, {3, 3}, {4, 2},
		{5, 3}, {6, 4}, {7, 6}, {8, 4}, {9, 3},
		{10, 4}, {11, 6}, {12, 8}, {13, 6}, {14, 4},
		{15, 3}, {16, 4}, {17, 6}, {18, 4}, {19, 3},
		{20, 2}, {21, 3}, {22, 4}, {23, 3}, {24, 2},
	}

	for _, tt := range tests {
		t.Run("pos_"+string(rune('0'+tt.pos)), func(t *testing.T) {
			assert.Equal(t, tt.expected, g.GetDegree(tt.pos), "GetDegree(%d)", tt.pos)
		})
	}
}

func TestGetNeighborMask(t *testing.T) {
	g := New(5)

	tests := []struct {
		pos      int
		expected uint64
	}{
		{0, (1 << 7) | (1 << 11)},
		{12, (1 << 1) | (1 << 3) | (1 << 5) | (1 << 9) | (1 << 15) | (1 << 19) | (1 << 21) | (1 << 23)},
	}

	for _, tt := range tests {
		t.Run("pos_"+string(rune('0'+tt.pos)), func(t *testing.T) {
			mask := g.GetNeighborMask(tt.pos)
			assert.Equal(t, state.State(tt.expected), mask, "GetNeighborMask(%d)", tt.pos)
		})
	}
}

func TestNeighborsWithinBounds(t *testing.T) {
	g := New(5)

	for pos := range 25 {
		neighbors := g.GetNeighbors(pos)
		for _, n := range neighbors {
			assert.True(t, n >= 0 && n < 25, "GetNeighbors(%d) in bounds", pos)

			nx, ny := n/5, n%5
			x, y := pos/5, pos%5
			dx, dy := abs(nx-x), abs(ny-y)
			assert.True(t, (dx == 2 && dy == 1) || (dx == 1 && dy == 2),
				"GetNeighbors(%d) knight move", pos)
		}
	}
}

func TestNeighborSymmetry(t *testing.T) {
	g := New(5)

	for pos := range 25 {
		mask := g.GetNeighborMask(pos)
		neighbors := g.GetNeighbors(pos)
		expectedMask := state.State(0)
		for _, n := range neighbors {
			expectedMask = expectedMask.Union(state.State(1 << uint64(n)))
		}
		assert.Equal(t, expectedMask, mask, "GetNeighborMask(%d) matches GetNeighbors", pos)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
