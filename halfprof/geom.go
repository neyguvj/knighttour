package halfprof

// geom.go — geometry of the horizontal cut between rows ⌊n/2⌋−1 and ⌊n/2⌋
// (0-based): the upper half covers rows [0,h), the lower half rows [h,n).
// Knight edges crossing the boundary belong to the lower half by the source
// convention; every such edge touches the upper side only in rows h−2 and
// h−1, so the seam frame is exactly those two upper rows.

import (
	"cmp"
	"slices"

	"knighttour/graph"
)

// cutGeom is the immutable board geometry of one half-split: cell numbering
// pos = row*n + col (the graph package's x-major order), per-cell knight
// neighbours split by ownership, and the ordered seam frame of the upper side.
type cutGeom struct {
	upCells   []int
	loCells   []int
	upAdj     [][]int
	loAdj     [][]int
	loCross   [][]int
	seam      []int
	seamIdx   []int
	downCross []bool
	n         int
	h         int
	total     int
}

// newCutGeom builds the split geometry of an n×n board.
func newCutGeom(n int) *cutGeom {
	board := graph.New(n)
	g := &cutGeom{n: n, h: n / 2, total: n * n}
	g.seamIdx = make([]int, g.total)
	g.downCross = make([]bool, g.total)
	for i := range g.seamIdx {
		g.seamIdx[i] = -1
	}
	if g.h >= 2 {
		g.addSeamRow(g.h - 2)
	}
	g.addSeamRow(g.h - 1)

	for cell := range g.total {
		if g.inUpper(cell) {
			g.upCells = append(g.upCells, cell)
		} else {
			g.loCells = append(g.loCells, cell)
		}
	}
	// Processing most-constrained cells first (ascending in-half knight degree)
	// prunes the path-cover DFS dramatically; profile extraction never depends
	// on this order (it works through seam indices and component walks).
	g.orderHalf(board, g.upCells, true)
	g.orderHalf(board, g.loCells, false)

	posOf := make([]int, g.total)
	for i, c := range g.upCells {
		posOf[c] = i
	}
	for _, c := range g.upCells {
		var adj []int
		for _, nb := range board.GetNeighbors(c) {
			if g.inUpper(nb) && posOf[nb] > posOf[c] {
				adj = append(adj, posOf[nb])
			}
		}
		g.upAdj = append(g.upAdj, adj)
	}
	g.buildLoAdj(board, posOf)
	return g
}

// orderHalf sorts half cells by ascending in-half knight degree, ties by cell.
func (g *cutGeom) orderHalf(board *graph.Graph, cells []int, up bool) {
	slices.SortFunc(cells, func(a, b int) int {
		da, db := g.halfDeg(board, a, up), g.halfDeg(board, b, up)
		if c := cmp.Compare(da, db); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
}

// halfDeg counts the neighbours of cell that lie in the same half.
func (g *cutGeom) halfDeg(board *graph.Graph, cell int, up bool) int {
	d := 0
	for _, nb := range board.GetNeighbors(cell) {
		if g.inUpper(nb) == up {
			d++
		}
	}
	return d
}

// buildLoAdj fills the lower half's later-neighbour lists and the crossing
// partner lists (marking seam realizability), consuming posOf as scratch.
func (g *cutGeom) buildLoAdj(board *graph.Graph, posOf []int) {
	for i, c := range g.loCells {
		posOf[c] = i
	}
	for _, c := range g.loCells {
		var adj, cross []int
		for _, nb := range board.GetNeighbors(c) {
			switch {
			case !g.inUpper(nb) && posOf[nb] > posOf[c]:
				adj = append(adj, posOf[nb])
			case g.inUpper(nb):
				cross = append(cross, nb)
				g.downCross[nb] = true
			}
		}
		g.loAdj = append(g.loAdj, adj)
		g.loCross = append(g.loCross, cross)
	}
}

// addSeamRow appends one board row to the ordered seam frame.
func (g *cutGeom) addSeamRow(row int) {
	for c := range g.n {
		cell := row*g.n + c
		g.seamIdx[cell] = len(g.seam)
		g.seam = append(g.seam, cell)
	}
}

// inUpper reports whether the global cell lies in the upper half.
func (g *cutGeom) inUpper(cell int) bool { return cell/g.n < g.h }

// seamCell reports whether the global upper cell can host seam defects.
func (g *cutGeom) seamCell(cell int) bool { return g.seamIdx[cell] >= 0 }

// hasDownCross reports whether any crossing edge can land on upper cell c.
func (g *cutGeom) hasDownCross(cell int) bool { return g.downCross[cell] }
