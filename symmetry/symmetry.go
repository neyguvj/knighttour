package symmetry

import (
	"sort"

	"knighttour/path"
	"knighttour/state"
)

type Transform func(x, y, size int) (int, int)

type Symmetry struct {
	size                int
	transforms          []Transform
	canonicalTransforms []Transform
	bestTransforms      [][]Transform
	canonical           []int
	orbitSize           []int
	groups              []CanonicalGroup
}

func NewSymmetry(size int) *Symmetry {
	s := &Symmetry{
		size:       size,
		transforms: GetSymmetries(size),
	}

	totalCells := size * size

	s.canonical = make([]int, totalCells)
	s.canonicalTransforms = make([]Transform, totalCells)
	s.orbitSize = make([]int, totalCells)

	for pos := range totalCells {
		s.canonical[pos], s.canonicalTransforms[pos] = s.getCanonicalPositionAndTransform(pos)
		s.orbitSize[pos] = s.computeOrbitSize(pos)
	}

	s.bestTransforms = make([][]Transform, totalCells)
	for start := range totalCells {
		s.bestTransforms[start] = make([]Transform, totalCells)
		for end := range totalCells {
			bestStart := applyTransform(s.canonicalTransforms[start], start, size)
			bestEnd := end
			s.bestTransforms[start][end] = s.canonicalTransforms[start]
			for _, transform := range s.transforms {
				newStart := applyTransform(transform, start, size)
				newEnd := applyTransform(transform, end, size)
				if newStart == bestStart && newEnd < bestEnd {
					bestEnd = newEnd
					s.bestTransforms[start][end] = transform
				}
			}
		}
	}

	s.groups = s.buildCanonicalGroups()

	return s
}

func (s *Symmetry) applyTransform(t Transform, pos int) int {
	return applyTransform(t, pos, s.size)
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

func (s *Symmetry) CanonicalizePath(p path.Path) path.Path {
	bewstTransform := s.bestTransforms[p.Start()][p.End()]
	bestStart := s.applyTransform(bewstTransform, p.Start())
	bestEnd := s.applyTransform(bewstTransform, p.End())
	bestState := s.transformState(bewstTransform, p.State())

	return path.New(bestState, bestStart, bestEnd)
}

func (s *Symmetry) getCanonicalPositionAndTransform(pos int) (int, Transform) {
	x, y := pos/s.size, pos%s.size
	bestX, bestY := x, y
	bestTransform := s.transforms[0]
	for _, t := range s.transforms {
		nx, ny := t(x, y, s.size)
		if nx < bestX || (nx == bestX && ny < bestY) {
			bestX, bestY = nx, ny
			bestTransform = t
		}
	}

	return bestX*s.size + bestY, bestTransform
}

func (s *Symmetry) computeOrbitSize(pos int) int {
	seen := make(map[int]bool)

	for _, t := range s.transforms {
		transformed := s.applyTransform(t, pos)
		seen[transformed] = true
	}

	return len(seen)
}

func (s *Symmetry) buildCanonicalGroups() []CanonicalGroup {
	totalCells := s.size * s.size
	groupsMap := make(map[int]*CanonicalGroup)

	for pos := 0; pos < totalCells; pos++ {
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

	return groups
}

func (s *Symmetry) transformState(t Transform, st state.State) state.State {
	result := state.State(0)

	for pos := 0; pos < s.size*s.size; pos++ {
		if st.IsVisited(pos) {
			transformedPos := s.applyTransform(t, pos)
			result = result.Visit(transformedPos)
		}
	}

	return result
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

func applyTransform(t Transform, pos, size int) int {
	x := pos / size
	y := pos % size
	nx, ny := t(x, y, size)
	return nx*size + ny
}
