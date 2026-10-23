package split

import (
	"fmt"

	"knighttour/graph"
)

// halfWalk is the immutable geometry of one half-board frontier walk. Cells of
// the half are processed row-major; every internal edge is decided at its later
// endpoint and every seam edge incident to a cell is decided at that cell's
// step. A cell leaves the window after the step of its last internal neighbour
// (seam edges need no live slot: they are terminal occurrences, not edges inside
// the half), so live rows fit into three residues mod 3 and slots never shift.
type halfWalk struct {
	nbrs    [][]int   // per step: internal neighbour slots already processed
	stubs   [][]uint8 // per step: global seam-edge indices decided at this cell
	curSlot []int     // per step: slot receiving the current cell
	drops   [][]int   // per step: slots leaving the window right after it
	steps   int       // rows*cols processed cells
	nSlots  int       // 3*cols fixed window slots
}

// cutGeom is the immutable geometry of one balanced vertical cut c = ⌈n/2⌉:
// the seam edge list (each pair is (left endpoint, right endpoint)), the two
// half walks and the mirror permutation of seam indices for even n. The knight
// board itself comes from the graph package, so the move order and adjacency
// match the rest of the project by construction.
type cutGeom struct {
	seam    [][2]int // seam edges in deterministic order: row-major L cell, fixed neighbor order
	perCell [][]uint8
	mirror  []int // seam index → reflected seam index; nil for odd n
	left    halfWalk
	right   halfWalk
	n       int
	cutCol  int
}

// newCutGeom builds the cut geometry for an n×n board. The seam of a balanced
// cut holds exactly 6n−8 edges for n ≥ 4 (columns (c−1,c) give 2(n−2), the two
// distance-2 column pairs 2(n−1) each); smaller boards carry fewer and are not
// bound by that formula.
func newCutGeom(n int) *cutGeom {
	g := &cutGeom{n: n, cutCol: (n + 1) / 2}
	board := graph.New(n)
	g.buildSeam(board)
	g.left = newHalfWalk(g, board, 0, g.cutCol)
	g.right = newHalfWalk(g, board, g.cutCol, n)
	if n%2 == 0 {
		g.buildMirror()
	}
	return g
}

// buildSeam enumerates the seam edges: every knight edge with its left endpoint
// in L (col < c) and right endpoint in R (col ≥ c), listed by row-major left
// endpoint and the fixed neighbor order of the graph package. Each seam edge
// has exactly one endpoint on each side, so the per-cell incidence lists
// partition the indices on both sides.
func (g *cutGeom) buildSeam(board *graph.Graph) {
	g.perCell = make([][]uint8, g.n*g.n)
	for v := range g.n * g.n {
		if v%g.n >= g.cutCol {
			continue
		}
		for _, w := range board.GetNeighbors(v) {
			if w%g.n < g.cutCol {
				continue
			}
			idx := uint8(len(g.seam))
			g.seam = append(g.seam, [2]int{v, w})
			g.perCell[v] = append(g.perCell[v], idx)
			g.perCell[w] = append(g.perCell[w], idx)
		}
	}
}

// newHalfWalk builds the walk of the half spanning columns [lo,hi). Neighbours
// are only internal edges (both endpoints inside the half); a cell's deadline is
// the latest step among its internal neighbours, or its own step when it has none.
func newHalfWalk(g *cutGeom, board *graph.Graph, lo, hi int) halfWalk {
	cols := hi - lo
	w := halfWalk{steps: g.n * cols, nSlots: 3 * cols}
	if cols <= 0 {
		return w
	}
	w.curSlot = make([]int, w.steps)
	w.nbrs = make([][]int, w.steps)
	w.stubs = make([][]uint8, w.steps)
	w.drops = make([][]int, w.steps)
	slotOf := func(r, c int) int { return (r%3)*cols + (c - lo) }
	for st := range w.steps {
		r, c := st/cols, st%cols+lo
		w.curSlot[st] = slotOf(r, c)
		w.stubs[st] = g.perCell[r*g.n+c]
		deadline := st
		for _, nb := range board.GetNeighbors(r*g.n + c) {
			nc := nb % g.n
			if nc < lo || nc >= hi {
				continue
			}
			nr := nb / g.n
			ns := nr*cols + (nc - lo)
			if ns < st {
				w.nbrs[st] = append(w.nbrs[st], slotOf(nr, nc))
			}
			deadline = max(deadline, ns)
		}
		w.drops[deadline] = append(w.drops[deadline], w.curSlot[st])
	}
	return w
}

// buildMirror fills the reflection permutation of seam indices for even n: the
// vertical midline reflection maps (r, j) to (r, n−1−j), swaps the halves and
// maps every seam edge to a seam edge. The pair (vL, vR) therefore reflects to
// (reflect(vR), reflect(vL)) in (L, R) endpoint order. A missing image means the
// geometry construction regressed: the reflection of a knight edge is always a
// knight edge across the invariant cut.
func (g *cutGeom) buildMirror() {
	byEnds := make(map[[2]int]int, len(g.seam))
	for i, e := range g.seam {
		byEnds[e] = i
	}
	refl := func(v int) int { return v/g.n*g.n + (g.n - 1 - v%g.n) }
	g.mirror = make([]int, len(g.seam))
	for i, e := range g.seam {
		j, ok := byEnds[[2]int{refl(e[1]), refl(e[0])}]
		if !ok {
			panic(fmt.Sprintf("split: reflection of seam edge %v is not a seam edge", e))
		}
		g.mirror[i] = j
	}
}
