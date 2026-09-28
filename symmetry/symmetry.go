package symmetry

import (
	"math/bits"
	"sort"

	"knighttour/state"
)

// Transform maps one board cell (x, y) under a D4 element for a board of the
// given size. Closures are invoked only while building the perms LUT.
type Transform func(x, y, size int) (int, int)

const numTransforms = 8

// NumTransforms is the size of the D4 group (exported for batch APIs).
const NumTransforms = numTransforms
const maxCells = 64

// Symmetry holds the D4 action on board cells as precomputed permutation
// tables: perms[t][cell] is the image of cell under transform t, compose[i][j]
// the index of perms[i] ∘ perms[j]. The hot canonicalization path is pure
// table lookups — no closures, allocations or recursion.
type Symmetry struct {
	canonical []int
	orbitSize []int
	groups    []CanonicalGroup
	size      int
	perms     [numTransforms][maxCells]uint8
	compose   [numTransforms][numTransforms]uint8
}

// NewSymmetry builds the D4 permutation tables for a size×size board together
// with the per-cell canonical positions, orbit sizes and start groups.
func NewSymmetry(size int) *Symmetry {
	s := &Symmetry{size: size}

	totalCells := size * size

	closures := GetSymmetries()
	for t, f := range closures {
		for pos := range totalCells {
			x, y := pos/size, pos%size
			nx, ny := f(x, y, size)
			s.perms[t][pos] = uint8(nx*size + ny)
		}
	}
	s.buildCompose()

	s.canonical = make([]int, totalCells)
	s.orbitSize = make([]int, totalCells)

	for pos := range totalCells {
		s.canonical[pos] = s.getCanonicalPosition(pos)
		s.orbitSize[pos] = s.computeOrbitSize(pos)
	}

	s.groups = s.buildCanonicalGroups()
	return s
}

// GetCanonicalPosition returns the D4-orbit minimum cell index of pos.
func (s *Symmetry) GetCanonicalPosition(pos int) int {
	return s.canonical[pos]
}

// IsCanonicalPosition reports whether pos is the minimum of its own orbit.
func (s *Symmetry) IsCanonicalPosition(pos int) bool {
	return s.GetCanonicalPosition(pos) == pos
}

// GetOrbitSize returns the size of the cell orbit of pos under D4.
func (s *Symmetry) GetOrbitSize(pos int) int {
	return s.orbitSize[pos]
}

// CanonicalGroup is one cell orbit: its members, the canonical (minimum)
// representative and the orbit size.
type CanonicalGroup struct {
	Positions []int
	Canonical int
	OrbitSize int
}

// GetCanonicalGroups returns the cell orbits sorted by canonical position.
func (s *Symmetry) GetCanonicalGroups() []CanonicalGroup {
	return s.groups
}

// CanonicalMaskFrame returns the canonical mask K = min_t t(st), the frame —
// index of any transform that reached the minimum, deterministically the
// smallest such index — and stab, the bitmask of the stabilizer Stab(K)
// (bit i ⟺ transform i fixes K; contains the identity and is closed under
// composition — a subgroup). The set {t : t(st) == K} is the coset
// {s ∘ frame : s ∈ Stab(K)} (specs/symmetry.md).
//
//nolint:gocritic // captLocal: K is the canonical-mask symbol of specs/symmetry.md.
func (s *Symmetry) CanonicalMaskFrame(st state.State) (K state.State, frame uint8, stab uint8) {
	var imgs [numTransforms]state.State
	for t := range imgs {
		imgs[t] = s.transformState(uint8(t), st)
	}

	K, frame = imgs[0], 0
	for t := 1; t < numTransforms; t++ {
		if imgs[t] < K {
			K, frame = imgs[t], uint8(t)
		}
	}

	// t ∈ Stab(K) ⟺ t(K) == K ⟺ (t ∘ frame)(st) == K — one table hop plus a
	// compare per element, no extra mask transforms.
	for t := range numTransforms {
		if imgs[s.compose[t][frame]] == K {
			stab |= 1 << t
		}
	}
	return K, frame, stab
}

// TransformCell returns the image of cell under transform t (a perms LUT
// lookup), moving an end from st's coordinate system into K's.
func (s *Symmetry) TransformCell(t uint8, cell int) int {
	return int(s.perms[t][cell])
}

// ClassRep returns the class representative of a cell: the minimum of its
// stab-orbit {s(cell) : s ∈ Stab}. Idempotent; the uint8 type is the packed
// rep:8 tag of table values (specs/cache.md). Cells related by the
// stabilizer share one rep, unrelated cells never do (specs/symmetry.md).
func (s *Symmetry) ClassRep(stab uint8, cell int) uint8 {
	rep := uint8(cell)
	for m := stab; m != 0; m &= m - 1 {
		if c := s.perms[bits.TrailingZeros8(m)][cell]; c < rep {
			rep = c
		}
	}
	return rep
}

// CellOrbitSize returns the D4 orbit size of the pair (K, cell):
// 8 / |{s ∈ stab : s(cell) == cell}|. Constant on the cell's class and a
// power of two from {1, 2, 4, 8}, so dividing an accumulated class weight by
// it is exact (specs/symmetry.md).
func (s *Symmetry) CellOrbitSize(stab uint8, cell int) int {
	fixed := 0
	for m := stab; m != 0; m &= m - 1 {
		if s.perms[bits.TrailingZeros8(m)][cell] == uint8(cell) {
			fixed++
		}
	}
	return numTransforms / fixed
}

// CanonicalClass is the shared writer/reader canonicalization: the canonical
// mask, the end's class tag and the pair orbit size. Composition
// CanonicalMaskFrame → TransformCell(frame, end) → ClassRep/CellOrbitSize;
// (K, rep) is the lexicographic minimum of (t(st), t(end)) over D4 and the
// result does not depend on which argmin frame was picked.
//
//nolint:gocritic // captLocal: K is the canonical-mask symbol of specs/symmetry.md.
func (s *Symmetry) CanonicalClass(st state.State, end int) (K state.State, rep uint8, orbitSize int) {
	K, frame, stab := s.CanonicalMaskFrame(st)
	c := s.TransformCell(frame, end)
	return K, s.ClassRep(stab, c), s.CellOrbitSize(stab, c)
}

func (s *Symmetry) transformState(t uint8, st state.State) state.State {
	perm := &s.perms[t]
	result := state.NewState()
	for pos := range st.AllVisited() {
		result = result.Visit(int(perm[pos]))
	}
	return result
}

// buildCompose fills compose[i][j] with the index of perms[i] ∘ perms[j];
// D4 is closed, so every composition is one of the eight table rows.
func (s *Symmetry) buildCompose() {
	for i := range numTransforms {
		for j := range numTransforms {
			s.compose[i][j] = s.indexOfComposition(uint8(i), uint8(j))
		}
	}
}

// indexOfComposition finds the index k with perms[k] == perms[i] ∘ perms[j]
// on every board cell.
func (s *Symmetry) indexOfComposition(i, j uint8) uint8 {
	for k := range numTransforms {
		if s.composedEqual(i, j, uint8(k)) {
			return uint8(k)
		}
	}
	return 0 // unreachable: the D4 table is closed under composition
}

// composedEqual reports perms[k](c) == perms[i](perms[j](c)) for every cell.
func (s *Symmetry) composedEqual(i, j, k uint8) bool {
	for c := range s.size * s.size {
		if s.perms[k][c] != s.perms[i][s.perms[j][c]] {
			return false
		}
	}
	return true
}

// getCanonicalPosition returns the minimum cell index in pos's D4 orbit.
func (s *Symmetry) getCanonicalPosition(pos int) int {
	bestPos := pos
	for t := range numTransforms {
		if p := s.perms[t][pos]; int(p) < bestPos {
			bestPos = int(p)
		}
	}
	return bestPos
}

// computeOrbitSize counts the distinct images of pos under the eight perms.
func (s *Symmetry) computeOrbitSize(pos int) int {
	seen := make(map[int]bool)
	for t := range numTransforms {
		seen[s.TransformCell(uint8(t), pos)] = true
	}
	return len(seen)
}

// buildCanonicalGroups partitions all board cells into D4 orbits keyed by
// their canonical position, sorted by that key.
func (s *Symmetry) buildCanonicalGroups() []CanonicalGroup {
	totalCells := s.size * s.size
	groupsMap := make(map[int]*CanonicalGroup)

	for pos := range totalCells {
		canonical := s.canonical[pos]

		if groupsMap[canonical] == nil {
			groupsMap[canonical] = &CanonicalGroup{
				Canonical: canonical,
				Positions: []int{},
			}
		}

		groupsMap[canonical].Positions = append(groupsMap[canonical].Positions, pos)
	}

	groups := make([]CanonicalGroup, 0, len(groupsMap))
	for _, g := range groupsMap {
		g.OrbitSize = len(g.Positions)
		sort.Ints(g.Positions)
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Canonical < groups[j].Canonical
	})
	return groups
}

// GetSymmetries returns the eight D4 transform closures in the table order of
// specs/symmetry.md; they are called only while building the perms LUT.
func GetSymmetries() []Transform {
	return []Transform{
		func(x, y, s int) (int, int) { return x, y },
		func(x, y, s int) (int, int) { return y, s - 1 - x },
		func(x, y, s int) (int, int) { return s - 1 - x, s - 1 - y },
		func(x, y, s int) (int, int) { return s - 1 - y, x },
		func(x, y, s int) (int, int) { return x, s - 1 - y },
		func(x, y, s int) (int, int) { return s - 1 - x, y },
		func(x, y, s int) (int, int) { return y, x },
		func(x, y, s int) (int, int) { return s - 1 - y, s - 1 - x },
	}
}
