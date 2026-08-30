package symmetry

import (
	"sort"

	"knighttour/path"
	"knighttour/state"
)

type Transform func(x, y, size int) (int, int)

const numTransforms = 8
const maxCells = 64

type Symmetry struct {
	size         int
	perms        [numTransforms][maxCells]uint8
	canonical    []int
	canonicalIdx []uint8
	orbitSize    []int
	bestIdx      [][]uint8
	groups       []CanonicalGroup
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
	s.canonicalIdx = make([]uint8, totalCells)
	s.orbitSize = make([]int, totalCells)

	for pos := range totalCells {
		s.canonical[pos], s.canonicalIdx[pos] = s.getCanonicalPositionAndTransform(pos)
		s.orbitSize[pos] = s.computeOrbitSize(pos)
	}

	s.bestIdx = make([][]uint8, totalCells)
	for start := range totalCells {
		s.bestIdx[start] = make([]uint8, totalCells)
		bestStart := s.canonical[start]
		for end := range totalCells {
			bestT := s.canonicalIdx[start]
			bestEnd := end
			for t := range numTransforms {
				if int(s.perms[t][start]) != bestStart {
					continue
				}
				newEnd := int(s.perms[t][end])
				if newEnd < bestEnd {
					bestEnd = newEnd
					bestT = uint8(t)
				}
			}
			s.bestIdx[start][end] = bestT
		}
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
	Canonical int
	OrbitSize int
	Positions []int
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

func (s *Symmetry) CanonicalizePath(p path.Path) path.Path {
	t := s.bestIdx[p.Start()][p.End()]

	return path.New(
		s.transformState(t, p.State()),
		int(s.perms[t][p.Start()]),
		int(s.perms[t][p.End()]),
	)
}

func (s *Symmetry) transformState(t uint8, st state.State) state.State {
	perm := &s.perms[t]
	result := state.NewState()
	for pos := range st.AllVisited() {
		result = result.Visit(int(perm[pos]))
	}
	return result
}

func applyTransform(t Transform, pos, size int) int {
	x := pos / size
	y := pos % size
	nx, ny := t(x, y, size)
	return nx*size + ny
}

func (s *Symmetry) getCanonicalPositionAndTransform(pos int) (bestPos int, bestT uint8) {
	bestPos = pos
	bestT = uint8(0)
	for t := range numTransforms {
		if p := s.perms[t][pos]; int(p) < bestPos {
			bestPos = int(p)
			bestT = uint8(t)
		}
	}
	return bestPos, bestT
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
