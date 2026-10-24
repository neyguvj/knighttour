package zdd

import (
	"slices"

	"knighttour/graph"
)

// halfGeom is one half of a cut knight board in BuildHalf input form: local
// vertex ids are assigned row-major over the half's columns, stub edges run
// to virtual leaves above nVerts, and edgeIDs carry the global board edge
// numbering shared by both halves (a seam edge keeps the same id on both
// sides, which is what makes node reuse across halves possible).
type halfGeom struct {
	edges   [][2]int
	stubOf  []int32
	edgeIDs []int32
	nVerts  int
}

// boardHalves cuts an n×n knight board at c = ⌈n/2⌉ and returns the two
// halves plus the seam list in split's enumeration order (row-major left
// endpoint, fixed neighbor order). Global edge ids follow one deterministic
// scan of every undirected knight edge.
func boardHalves(n int) (geomL, geomR halfGeom, seam [][2]int) {
	board := graph.New(n)
	c := (n + 1) / 2

	perCell := make([][]int32, n*n)
	for v := range n * n {
		if v%n >= c {
			continue
		}
		for _, w := range board.GetNeighbors(v) {
			if w%n < c {
				continue
			}
			idx := int32(len(seam))
			seam = append(seam, [2]int{v, w})
			perCell[v] = append(perCell[v], idx)
			perCell[w] = append(perCell[w], idx)
		}
	}

	gid := make(map[[2]int]int32, n*n*4)
	var next int32
	edgeID := func(a, b int) int32 {
		if a > b {
			a, b = b, a
		}
		if id, ok := gid[[2]int{a, b}]; ok {
			return id
		}
		id := next
		gid[[2]int{a, b}] = id
		next++
		return id
	}
	for v := range n * n {
		for _, w := range board.GetNeighbors(v) {
			if v < w {
				edgeID(v, w) // pre-assign internal ids in one canonical scan
			}
		}
	}

	return halfSide(board, n, 0, c, seam, perCell, edgeID),
		halfSide(board, n, c, n, seam, perCell, edgeID),
		seam
}

// halfSide builds the half spanning columns [lo, hi): internal knight edges
// (enumerated once) plus one stub edge per seam incidence of a half cell;
// the real end of such an incidence is the cell itself.
func halfSide(board *graph.Graph, n, lo, hi int, seam [][2]int, perCell [][]int32, edgeID func(a, b int) int32) halfGeom {
	cols := hi - lo
	local := func(v int) int { return (v/n)*cols + v%n - lo }
	g := halfGeom{nVerts: n * cols}
	nStub := 0
	for v := range n * n {
		if v%n < lo || v%n >= hi {
			continue
		}
		for _, w := range board.GetNeighbors(v) {
			if wc := w % n; wc >= lo && wc < hi && v < w {
				g.edges = append(g.edges, [2]int{local(v), local(w)})
				g.stubOf = append(g.stubOf, -1)
				g.edgeIDs = append(g.edgeIDs, edgeID(v, w))
			}
		}
		for _, sidx := range perCell[v] {
			e := seam[sidx]
			stubVert := g.nVerts + nStub
			nStub++
			g.edges = append(g.edges, [2]int{local(v), stubVert})
			g.stubOf = append(g.stubOf, sidx)
			g.edgeIDs = append(g.edgeIDs, edgeID(e[0], e[1]))
		}
	}
	return g
}

// nTotal returns the endpoint id bound (real vertices plus virtual leaves).
func (g halfGeom) nTotal() int {
	total := g.nVerts
	for _, s := range g.stubOf {
		if s >= 0 {
			total++
		}
	}
	return total
}

// narrowOrder returns the deterministic narrow edge order of the geometry,
// computed outside the timed census loops (the zdd census convention).
func (g halfGeom) narrowOrder() []int { return OrderNarrow(g.nTotal(), g.edges) }

// buildHalf orders the geometry with OrderNarrow and runs it into the session.
func (g halfGeom) buildHalf(s *HalfSession) *HalfDiagram {
	hd, err := s.BuildHalf(g.nVerts, g.edges, g.stubOf, g.edgeIDs, g.narrowOrder())
	if err != nil {
		panic(err)
	}
	return hd
}

// mirrorPerm returns the seam-index reflection ρ of an even-n vertical midline
// cut ((r, j) → (r, n−1−j)): a seam edge (vL, vR) reflects to the seam edge
// (ρvR, ρvL), so ρ maps each seam index to its mirror image. Odd n has no such
// symmetry and returns nil.
func mirrorPerm(n int, seam [][2]int) []int {
	if n%2 != 0 {
		return nil
	}
	refl := func(v int) int { return (v/n)*n + (n - 1 - v%n) }
	idx := make(map[[2]int]int, len(seam))
	for i, e := range seam {
		a, b := e[0], e[1]
		if a > b {
			a, b = b, a
		}
		idx[[2]int{a, b}] = i
	}
	perm := make([]int, len(seam))
	for i, e := range seam {
		r := [2]int{refl(e[1]), refl(e[0])} // (left, right) endpoint order again
		a, b := r[0], r[1]
		if a > b {
			a, b = b, a
		}
		id, ok := idx[[2]int{a, b}]
		if !ok {
			panic("zdd: mirror image of a seam edge is not a seam edge")
		}
		perm[i] = id
	}
	return perm
}

// reflectSigmas maps a whole σ distribution through ρ (a permutation, so keys
// stay distinct and weights pass through unchanged).
func reflectSigmas(m map[Sigma]uint64, perm []int) map[Sigma]uint64 {
	out := make(map[Sigma]uint64, len(m))
	for sg, w := range m {
		out[reflectSigma(sg, perm)] += w
	}
	return out
}

// reflectSigma maps one interface state through the seam reflection ρ
// (N_R(σ) = N_L(ρσ), the split mirror convention): stub terminals are renamed,
// global ends stay, pairs are re-sorted and repacked canonically.
func reflectSigma(sg Sigma, perm []int) Sigma {
	pairs := sg.pairs()
	words := make([]uint16, 0, len(pairs))
	for _, p := range pairs {
		a, b := p[0], p[1]
		if a != termGlobal {
			a = uint8(perm[a])
		}
		if b != termGlobal {
			b = uint8(perm[b])
		}
		words = append(words, termWord(a, b))
	}
	slices.Sort(words)
	var out Sigma
	for i, w := range words {
		out.words[i/5] |= uint64(w) << (pairBits * (i % 5))
		t1, t2 := uint8(w>>6), uint8(w&63)
		if t1 != termGlobal {
			out.sMask |= 1 << t1
		}
		if t2 != termGlobal {
			out.sMask |= 1 << t2
		}
	}
	out.ends = sg.ends
	return out
}
