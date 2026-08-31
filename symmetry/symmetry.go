package symmetry

import (
	"sort"

	"knighttour/path"
	"knighttour/state"
)

type Transform func(x, y, size int) (int, int)

const numTransforms = 8

// NumTransforms is the size of the D4 group (exported for batch APIs).
const NumTransforms = numTransforms
const maxCells = 64

type Symmetry struct {
	canonical []int
	orbitSize []int
	groups    []CanonicalGroup
	size      int
	perms     [numTransforms][maxCells]uint8
}

func NewSymmetry(size int) *Symmetry {
	s := &Symmetry{size: size}

	totalCells := size * size

	closures := GetSymmetries(size)
	for t, f := range closures {
		for pos := range totalCells {
			x, y := pos/size, pos%size
			nx, ny := f(x, y, size)
			s.perms[t][pos] = uint8(nx*size + ny)
		}
	}

	s.canonical = make([]int, totalCells)
	s.orbitSize = make([]int, totalCells)

	for pos := range totalCells {
		s.canonical[pos] = s.getCanonicalPosition(pos)
		s.orbitSize[pos] = s.computeOrbitSize(pos)
	}

	s.groups = s.buildCanonicalGroups()
	return s
}

func (s *Symmetry) applyIdx(t uint8, pos int) int {
	return int(s.perms[t][pos])
}

func (s *Symmetry) GetCanonicalPosition(pos int) int {
	return s.canonical[pos]
}

func (s *Symmetry) IsCanonicalPosition(pos int) bool {
	return s.GetCanonicalPosition(pos) == pos
}

func (s *Symmetry) GetOrbitSize(pos int) int {
	return s.orbitSize[pos]
}

type CanonicalGroup struct {
	Positions []int
	Canonical int
	OrbitSize int
}

func (s *Symmetry) GetCanonicalGroups() []CanonicalGroup {
	return s.groups
}

func (s *Symmetry) GetCanonicalGroupByPosition(pos int) CanonicalGroup {
	canonicalPos := s.GetCanonicalPosition(pos)
	for _, g := range s.groups {
		if g.Canonical == canonicalPos {
			return g
		}
	}
	return s.groups[0]
}

// Canonicalize returns the canonical representative of the D4 orbit of the
// pair (state, end): lexicographic minimum of (t(state), t(end)) over all 8
// board symmetries. Start is not involved: completions depend only on the
// visited mask and the current cell.
func (s *Symmetry) Canonicalize(st state.State, end int) path.Path {
	canonical, _ := s.CanonicalizeWithOrbitSize(st, end)
	return canonical
}

// CanonicalizeWithOrbitSize returns the canonical pair plus the size of its
// D4 orbit (number of distinct transformed pairs). The cache value W(K) is a
// fiber sum over all orbit members, so per-node prefix multiplicity is
// recovered by exact division: h(U,u) = W(canon) / orbitSize.
func (s *Symmetry) CanonicalizeWithOrbitSize(st state.State, end int) (canonical path.Path, orbitSize int) {
	return s.CanonicalFromStates(s.TransformStates(st), end)
}

// TransformStates returns all D4 images of the mask in one pass. Pair groups
// that share a mask should transform once and reuse the array for every end.
func (s *Symmetry) TransformStates(st state.State) [NumTransforms]state.State {
	var out [NumTransforms]state.State
	for t := range out {
		out[t] = s.transformState(uint8(t), st)
	}
	return out
}

// CanonicalFromStates is CanonicalizeWithOrbitSize for a precomputed mask
// image array: identical result, only the end permutation work per call.
func (s *Symmetry) CanonicalFromStates(states [NumTransforms]state.State, end int) (canonical path.Path, orbitSize int) {
	var ends [numTransforms]uint8

	best := 0
	for t := range numTransforms {
		ends[t] = s.perms[t][end]
		if states[t] < states[best] || (states[t] == states[best] && ends[t] < ends[best]) {
			best = t
		}
	}

	for i := range numTransforms {
		unique := true
		for j := range i {
			if states[j] == states[i] && ends[j] == ends[i] {
				unique = false
				break
			}
		}
		if unique {
			orbitSize++
		}
	}

	return path.New(states[best], int(ends[best])), orbitSize
}

func (s *Symmetry) transformState(t uint8, st state.State) state.State {
	perm := &s.perms[t]
	result := state.NewState()
	for pos := range st.AllVisited() {
		result = result.Visit(int(perm[pos]))
	}
	return result
}

func (s *Symmetry) getCanonicalPosition(pos int) int {
	bestPos := pos
	for t := range numTransforms {
		if p := s.perms[t][pos]; int(p) < bestPos {
			bestPos = int(p)
		}
	}
	return bestPos
}

func (s *Symmetry) computeOrbitSize(pos int) int {
	seen := make(map[int]bool)
	for t := range numTransforms {
		seen[s.applyIdx(uint8(t), pos)] = true
	}
	return len(seen)
}

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

func GetSymmetries(size int) []Transform {
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
