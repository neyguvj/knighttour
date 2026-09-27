package state

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewState(t *testing.T) {
	s := NewState()
	assert.True(t, s.IsEmpty(), "NewState() should return empty state")
}

func TestIsVisited(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		pos      int
		expected bool
	}{
		{"empty state any position", 0, 0, false},
		{"empty state position 3", 0, 3, false},
		{"bit 0 set", 1, 0, true},
		{"bit 3 set", 8, 3, true},
		{"bit 63 set", State(1 << 63), 63, true},
		{"bit not set", 5, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.IsVisited(tt.pos), "IsVisited(%d)", tt.pos)
		})
	}
}

func TestVisit(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		pos      int
		expected State
	}{
		{"add bit 0 to empty", 0, 0, 1},
		{"add bit 3", 0, 3, 8},
		{"add existing bit", 5, 2, 5},
		{"add bit 63", State(0), 63, State(1 << 63)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.state.Visit(tt.pos)
			assert.Equal(t, tt.expected, result, "Visit(%d)", tt.pos)
		})
	}
}

func TestUnvisit(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		pos      int
		expected State
	}{
		{"clear bit 0", 1, 0, 0},
		{"clear bit 3", 8, 3, 0},
		{"clear one of multiple bits", 9, 3, 1},
		{"clear not set bit", 5, 1, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.state.Unvisit(tt.pos)
			assert.Equal(t, tt.expected, result, "Unvisit(%d)", tt.pos)
		})
	}
}

func TestIsFull(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		cells    int
		expected bool
	}{
		{"empty state", 0, 5, false},
		{"all 5 cells visited", 31, 5, true},
		{"all 6 cells visited", 63, 6, true},
		{"all 25 cells (5x5)", State((1 << 25) - 1), 25, true},
		{"all 36 cells (6x6)", (1 << 36) - 1, 36, true},
		{"all 49 cells (7x7)", State((1 << 49) - 1), 49, true},
		{"all 64 cells (8x8)", ^State(0), 64, true},
		{"not all cells visited", 30, 5, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.IsFull(tt.cells), "IsFull(%d)", tt.cells)
		})
	}
}

func TestCountBits(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		expected int
	}{
		{"empty state", 0, 0},
		{"one bit", 1, 1},
		{"all bits set", 255, 8},
		{"every other bit", 0x5555555555555555, 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.CountBits(), "CountBits()")
		})
	}
}

func TestIsUnvisited(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		pos      int
		expected bool
	}{
		{"empty state", 0, 0, true},
		{"bit not set", 5, 1, true},
		{"bit set", 5, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.IsUnvisited(tt.pos), "IsUnvisited(%d)", tt.pos)
		})
	}
}

func TestIntersect(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		mask     State
		expected State
	}{
		{"empty intersection", 5, 2, 0},
		{"same states", 7, 7, 7},
		{"intersection", 15, 3, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.Intersect(tt.mask), "Intersect(%v)", tt.mask)
		})
	}
}

func TestUnion(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		mask     State
		expected State
	}{
		{"union of empties", 0, 0, 0},
		{"union without overlap", 5, 2, 7},
		{"union with overlap", 7, 3, 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.Union(tt.mask), "Union(%v)", tt.mask)
		})
	}
}

func TestIsEmpty(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		expected bool
	}{
		{"empty state", 0, true},
		{"non-empty state", 1, false},
		{"many bits", 255, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.IsEmpty(), "IsEmpty()")
		})
	}
}

func TestTrailingZeroBits(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		expected uint
	}{
		{"empty state", 0, 64},
		{"bit 0 set", 1, 0},
		{"bit 3 set", 8, 3},
		{"bit 63 set", 1 << 63, 63},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.TrailingZeroBits(), "TrailingZeroBits()")
		})
	}
}

func TestAndNot(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		mask     State
		expected State
	}{
		{"no changes", 5, 0, 5},
		{"clear set bit", 5, 1, 4},
		{"clear not set bit", 4, 1, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.AndNot(tt.mask), "AndNot(%v)", tt.mask)
		})
	}
}

func TestInvert(t *testing.T) {
	tests := []struct {
		name     string
		state    State
		cells    int
		expected State
	}{
		{"invert empty", 0, 5, 31},
		{"invert full", 31, 5, 0},
		{"partial invert", 5, 5, 26},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.Invert(tt.cells), "Invert(%d)", tt.cells)
		})
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		state    State
	}{
		{"empty state", "0", 0},
		{"bit 0", "1", 1},
		{"bit 3", "1000", 8},
		{"multiple bits", "101", 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.String(), "String()")
		})
	}
}
