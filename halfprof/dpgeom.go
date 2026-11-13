package halfprof

// dpgeom.go — row walks of the profile-DP engine (plan 31 step 0). Each half
// is walked row-major from its outer border toward the seam: the upper half
// rows 0..h−1, the lower half rows n−1..h. Every internal edge is decided at
// its later endpoint and every crossing edge at its lower endpoint, so each
// forest is generated exactly once; a cell leaves the three-row window after
// the step of its last internal neighbour (crossings need no live slot: the
// upper side records its defect at drop, the lower closes the terminal at the
// choose time). Seam cells of the upper half carry their seam frame index so
// the drop rule can close defects; hasDownCross marks those allowed to.

import "knighttour/graph"

// dpCross is one crossing edge decided at a lower cell: its global id (the
// terminal identity termFirst+id) and the upper seam frame slot it feeds.
type dpCross struct {
	id uint64
	f  int
}

// dpDrop is one cell leaving the window after a step: its slot and the seam
// frame index whose defects it may close (−1 when none are allowed).
type dpDrop struct {
	slot    int
	defectF int
}

// dpWalk is the immutable geometry of one half-board DP walk for endpoints
// s,t: per step the receiving slot, already-processed internal neighbour
// slots, crossing edges to choose, drop rules, the degree target and the zero
// end terminal (dpTermNone except at s and t). crossF/crossLoPos describe the
// global crossing ids for profile densification (lower half only): the upper
// seam slot and the lower cell's position in the geometry order used by the
// enumerator's crossing numbering.
type dpWalk struct {
	curSlot []int
	nbrs    [][]int
	items   [][]int // per step: nbrs slots (≥ 0) then crossing items (< 0)
	drops   [][]dpDrop
	cross   [][]dpCross
	tgt     []int8
	term    []uint64
	steps   int
	lower   bool
}

// dpGeom pairs the two half walks of one board with the crossing metadata the
// final profile emission needs. The upper walk has no crossings; both share
// one key layout whose pair capacity covers either side.
type dpGeom struct {
	crossF    []int
	crossPos  []int
	up        dpWalk
	lo        dpWalk
	kl        dpKeyLayout
	seamCells int
}

// newDpGeom builds both half walks, assigns the crossing ids and computes the
// shared key layout for an n×n board with endpoints s,t. It reports false when
// the geometry does not fit the packed key representation (out-of-contract
// boards).
func newDpGeom(g *cutGeom, s, t int) (dpGeom, bool) {
	board := graph.New(g.n)
	dg := dpGeom{seamCells: len(g.seam)}
	dg.up = newDpWalk(g, board, s, t, false)
	dg.lo = newDpWalk(g, board, s, t, true)
	dg.buildCrossings(g, board)
	fillItems(&dg.up)
	fillItems(&dg.lo)
	var ok bool
	if dg.kl, ok = newDpKeyLayout(g.n, dg.seamCells, len(dg.crossF)); !ok {
		return dpGeom{}, false
	}
	return dg, true
}

// setStepTarget writes the degree target and zero-end terminal of one step:
// interior cells join with degree 2, the tour ends s,t with degree 1 carrying
// their terminal from birth.
func setStepTarget(tgt *int8, term *uint64, cell, s, t int) {
	*tgt, *term = 2, dpTermNone
	switch cell {
	case s:
		*tgt, *term = 1, dpTermSId
	case t:
		*tgt, *term = 1, dpTermTId
	}
}

// fillItems concatenates each step's internal neighbour slots and crossing
// items into one subset-enumeration list (slots ≥ 0, crossings < 0).
func fillItems(w *dpWalk) {
	for st := range w.steps {
		items := make([]int, 0, len(w.nbrs[st])+len(w.cross[st]))
		items = append(items, w.nbrs[st]...)
		for j := range w.cross[st] {
			items = append(items, crossItem(j))
		}
		w.items[st] = items
	}
}

// newDpWalk builds one half walk. The lower walk numbers steps from the board
// bottom row upward; its crossing lists are filled later by buildCrossings.
func newDpWalk(g *cutGeom, board *graph.Graph, s, t int, lower bool) dpWalk {
	rows := g.h
	if lower {
		rows = g.n - g.h
	}
	w := dpWalk{lower: lower, steps: rows * g.n}
	w.curSlot = make([]int, w.steps)
	w.nbrs = make([][]int, w.steps)
	w.items = make([][]int, w.steps)
	w.drops = make([][]dpDrop, w.steps)
	w.cross = make([][]dpCross, w.steps)
	w.tgt = make([]int8, w.steps)
	w.term = make([]uint64, w.steps)

	cellOf := func(st int) (cell, row int) {
		r := st / g.n
		if lower {
			r = g.n - 1 - r
		}
		return r*g.n + st%g.n, r
	}
	slotOf := func(r, c int) int { return (r%3)*g.n + c }

	for st := range w.steps {
		cell, row := cellOf(st)
		w.curSlot[st] = slotOf(row, cell%g.n)
		setStepTarget(&w.tgt[st], &w.term[st], cell, s, t)
		deadline := st
		for _, nb := range board.GetNeighbors(cell) {
			if g.inUpper(nb) == lower {
				continue // not in this half: crossing (lower) or out of walk
			}
			nr, nc := nb/g.n, nb%g.n
			ns := nr*g.n + nc
			if lower {
				ns = (g.n-1-nr)*g.n + nc
			}
			if ns < st {
				w.nbrs[st] = append(w.nbrs[st], slotOf(nr, nc))
			}
			deadline = max(deadline, ns)
		}
		defectF := -1
		if !lower && g.seamIdx[cell] >= 0 && g.downCross[cell] {
			defectF = g.seamIdx[cell]
		}
		w.drops[deadline] = append(w.drops[deadline], dpDrop{slot: w.curSlot[st], defectF: defectF})
	}
	return w
}

// buildCrossings assigns global ids to the lower walk's crossing edges (in
// step order) and fills the emission metadata: upper seam slot per id and the
// lower cell's position in the geometry order (the enumerator's sort key for
// numbering crossings at one upper cell).
func (dg *dpGeom) buildCrossings(g *cutGeom, board *graph.Graph) {
	loPos := make([]int, g.total)
	for i, c := range g.loCells {
		loPos[c] = i
	}
	dg.crossF, dg.crossPos = nil, nil
	for st := range dg.lo.steps {
		cell := (g.n-1-st/g.n)*g.n + st%g.n
		for _, nb := range board.GetNeighbors(cell) {
			if !g.inUpper(nb) {
				continue
			}
			id := uint64(len(dg.crossF))
			dg.crossF = append(dg.crossF, g.seamIdx[nb])
			dg.crossPos = append(dg.crossPos, loPos[cell])
			dg.lo.cross[st] = append(dg.lo.cross[st], dpCross{id: id, f: g.seamIdx[nb]})
		}
	}
}
