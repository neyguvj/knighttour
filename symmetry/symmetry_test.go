package symmetry

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/state"
)

func applyTransform(t Transform, pos int) int {
	const boardSize = 5
	x := pos / boardSize
	y := pos % boardSize
	nx, ny := t(x, y, boardSize)
	return nx*boardSize + ny
}

func TestNewSymmetry(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"5x5 board", 5, false},
		{"6x6 board", 6, false},
		{"7x7 board", 7, false},
		{"8x8 board", 8, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSymmetry(tt.size)
			if s == nil {
				t.Fatal("NewSymmetry returned nil")
			}
			assert.Equal(t, tt.size, s.size, "size")
			assert.Len(t, GetSymmetries(), numTransforms, "number of transforms")
			totalCells := tt.size * tt.size
			assert.Len(t, s.canonical, totalCells, "canonical length")
			assert.Len(t, s.orbitSize, totalCells, "orbitSize length")
			groups := s.GetCanonicalGroups()
			totalInGroups := 0
			for _, g := range groups {
				totalInGroups += len(g.Positions)
			}
			assert.Equal(t, totalCells, totalInGroups, "total positions in groups")
		})
	}
}

func TestGetSymmetries(t *testing.T) {
	assert.Len(t, GetSymmetries(), numTransforms, "number of symmetries")
}

func TestApplyTransform(t *testing.T) {
	s := NewSymmetry(5)

	tests := []struct {
		name     string
		pos      int
		wantSame bool
	}{
		{"identity transform (0)", 0, true},
		{"center position", 12, true},
		{"corner position", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.TransformCell(0, tt.pos)
			if result != tt.pos && !tt.wantSame {
				assert.Equal(t, tt.pos, result, "ApplyTransform(0, %d)", tt.pos)
			}
		})
	}

	t.Run("all transforms on corner", func(t *testing.T) {
		pos := 0
		results := make(map[int]bool)
		for i := range 8 {
			result := s.TransformCell(uint8(i), pos)
			results[result] = true
		}
		assert.Len(t, results, 4, "corner position unique transforms")
	})

	t.Run("all transforms on edge (non-corner)", func(t *testing.T) {
		pos := 1
		results := make(map[int]bool)
		for i := range 8 {
			result := s.TransformCell(uint8(i), pos)
			results[result] = true
		}
		assert.Len(t, results, 8, "edge position unique transforms")
	})

	t.Run("all transforms on diagonal (not center)", func(t *testing.T) {
		pos := 6 // (1,1)
		results := make(map[int]bool)
		for i := range 8 {
			result := s.TransformCell(uint8(i), pos)
			results[result] = true
		}
		assert.Len(t, results, 4, "diagonal position %d unique transforms", pos)
	})
}

func TestApplyTransformStatic(t *testing.T) {
	syms := GetSymmetries()

	tests := []struct {
		name     string
		pos      int
		expected int
	}{
		{"identity transform", 0, 0},
		{"90 degree rotation of 0", 0, 4},
		{"180 degree rotation of 0", 0, 24},
		{"270 degree rotation of 0", 0, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var transform Transform
			switch tt.name {
			case "identity transform":
				transform = syms[0]
			case "90 degree rotation of 0":
				transform = syms[1]
			case "180 degree rotation of 0":
				transform = syms[2]
			case "270 degree rotation of 0":
				transform = syms[3]
			}
			result := applyTransform(transform, tt.pos)
			assert.Equal(t, tt.expected, result, "%s: pos %d", tt.name, tt.pos)
		})
	}

	t.Run("transform composition - two 90 degree rotations = 180", func(t *testing.T) {
		pos := 7

		result1 := applyTransform(syms[1], pos)
		result2 := applyTransform(syms[1], result1)

		expected := applyTransform(syms[2], pos)

		assert.Equal(t, expected, result2, "two 90° rotations of %d", pos)
	})
}

func TestGetCanonicalPosition(t *testing.T) {
	s := NewSymmetry(5)

	tests := []struct {
		name     string
		pos      int
		expected int
	}{
		{"corner 0 is canonical", 0, 0},
		{"corner 4 maps to 0", 4, 0},
		{"corner 20 maps to 0", 20, 0},
		{"corner 24 maps to 0", 24, 0},
		{"center is canonical", 12, 12},
		{"edge position 1 is canonical", 1, 1},
		{"position 3 maps to 1", 3, 1},
		{"position 5 maps to 1", 5, 1},
		{"diagonal position 6 is canonical", 6, 6},
		{"position 8 maps to 6", 8, 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.GetCanonicalPosition(tt.pos)
			assert.Equal(t, tt.expected, result, "GetCanonicalPosition(%d)", tt.pos)
		})
	}
}

func TestIsCanonicalPosition(t *testing.T) {
	s := NewSymmetry(5)

	tests := []struct {
		name     string
		pos      int
		expected bool
	}{
		{"corner 0 is canonical", 0, true},
		{"corner 4 is not canonical", 4, false},
		{"center is canonical", 12, true},
		{"edge 1 is canonical", 1, true},
		{"diagonal 6 is canonical", 6, true},
		{"position 3 is not canonical", 3, false},
		{"position 8 is not canonical", 8, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.IsCanonicalPosition(tt.pos)
			assert.Equal(t, tt.expected, result, "IsCanonicalPosition(%d)", tt.pos)
		})
	}
}

func TestGetOrbitSize(t *testing.T) {
	s := NewSymmetry(5)

	tests := []struct {
		name     string
		pos      int
		expected int
	}{
		{"corner 0", 0, 4},
		{"corner 4", 4, 4},
		{"center 12", 12, 1},
		{"edge position 1", 1, 8},
		{"diagonal position 6", 6, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.GetOrbitSize(tt.pos)
			assert.Equal(t, tt.expected, result, "GetOrbitSize(%d)", tt.pos)
		})
	}

	t.Run("orbit size is at least 1 and at most 8", func(t *testing.T) {
		totalCells := s.size * s.size
		for i := range totalCells {
			size := s.GetOrbitSize(i)
			assert.True(t, size >= 1 && size <= 8, "orbit size for pos %d = %d in range [1,8]", i, size)
		}
	})
}

func TestGetCanonicalGroups(t *testing.T) {
	s := NewSymmetry(5)

	groups := s.GetCanonicalGroups()

	t.Run("groups are not empty", func(t *testing.T) {
		if len(groups) == 0 {
			assert.NotEmpty(t, groups, "canonical groups found")
		}
	})

	t.Run("all positions covered", func(t *testing.T) {
		totalCells := s.size * s.size
		count := 0
		for _, g := range groups {
			count += len(g.Positions)
		}
		assert.Equal(t, totalCells, count, "total positions in groups")
	})

	t.Run("orbit size matches group length", func(t *testing.T) {
		for _, g := range groups {
			assert.Len(t, g.Positions, g.OrbitSize, "group canonical=%d positions", g.Canonical)
		}
	})

	t.Run("all positions in group have same canonical", func(t *testing.T) {
		for _, g := range groups {
			canonical := g.Canonical
			for _, pos := range g.Positions {
				assert.Equal(t, canonical, s.GetCanonicalPosition(pos), "position %d canonical", pos)
			}
		}
	})

	t.Run("positions are sorted", func(t *testing.T) {
		for _, g := range groups {
			sorted := true
			for i := 1; i < len(g.Positions); i++ {
				if g.Positions[i] < g.Positions[i-1] {
					sorted = false
					break
				}
			}
			assert.True(t, sorted, "positions in group %d are sorted", g.Canonical)
		}
	})
}

func TestPropertyOrbitSizePlus(t *testing.T) {
	s := NewSymmetry(5)

	groups := s.GetCanonicalGroups()

	for _, g := range groups {
		canonical := g.Canonical
		orbitSize := s.GetOrbitSize(canonical)

		assert.Equal(t, len(g.Positions), orbitSize, "canonical %d orbitSize", canonical)

		for _, pos := range g.Positions {
			assert.Equal(t, canonical, s.GetCanonicalPosition(pos), "position %d canonical match", pos)
		}
	}
}

func TestPropertyTransformInvolutions(t *testing.T) {
	s := NewSymmetry(5)

	t.Run("identity is involution", func(t *testing.T) {
		for i := range 25 {
			r1 := s.TransformCell(0, i)
			assert.Equal(t, i, r1, "identity transform")
		}
	})

	t.Run("180 rotation twice = identity", func(t *testing.T) {
		for i := range 25 {
			r1 := s.TransformCell(2, i)
			r2 := s.TransformCell(2, r1)
			assert.Equal(t, i, r2, "180° twice = identity")
		}
	})

	t.Run("reflection twice = identity", func(t *testing.T) {
		for refIdx := 4; refIdx < 8; refIdx++ {
			for i := range 25 {
				r1 := s.TransformCell(uint8(refIdx), i)
				r2 := s.TransformCell(uint8(refIdx), r1)
				assert.Equal(t, i, r2, "reflection %d twice = identity", refIdx)
			}
		}
	})
}

func TestApplyTransformToStateWithLookup(t *testing.T) {
	s := NewSymmetry(5)

	t.Run("identity transform preserves state", func(t *testing.T) {
		st := state.NewState().Visit(0).Visit(7).Visit(12)
		result := s.transformState(0, st)
		assert.Equal(t, uint64(st), uint64(result), "identity transform preserves state")
	})

	t.Run("transform moves all visited cells", func(t *testing.T) {
		st := state.NewState().Visit(7)
		result := s.transformState(1, st)

		assert.Equal(t, 1, result.CountBits(), "transformed state bit count")

		pos7Transformed := s.TransformCell(1, 7)
		assert.True(t, result.IsVisited(pos7Transformed), "transformed position visited")
	})
}

func TestMultipleSizes(t *testing.T) {
	for _, size := range []int{5, 6, 7, 8} {
		t.Run(fmt.Sprintf("%dx%d", size, size), func(t *testing.T) {
			s := NewSymmetry(size)

			groups := s.GetCanonicalGroups()
			count := 0
			for _, g := range groups {
				count += len(g.Positions)
			}
			assert.Equal(t, size*size, count, "total positions in groups")
		})
	}
}
