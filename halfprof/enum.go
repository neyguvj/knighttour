package halfprof

// enum.go — canonical backtracking enumeration of one half's spanning linear
// forests and the seam profile they emit. Cells are processed in the fixed
// geometry order (ascending in-half degree, see geom.go); every internal edge
// is decided at its earlier endpoint, crossings at their lower endpoint, so
// each forest is generated exactly once. A union-find
// with an undo log rejects premature cycles. At the last cell the config is
// aggregated: the upper half emits (mask, own pairing P); the lower half turns
// its chosen crossing edges into the induced pairing Q over the same upper
// seam occurrence nodes.

import (
	"context"
	"slices"
)

// tickNodes bounds how often the enumeration checks ctx (counter modulo;
// cancellation latency in DFS steps — specs/pairs.md precedent).
const tickNodes = 1 << 14

// halfEnum is one enumeration run over a single half of the board.
type halfEnum struct {
	ctx      context.Context
	err      error
	g        *cutGeom
	table    profileSink
	deg      []int8
	nbrs     [][]int8
	adj      [][]int
	cross    [][]int
	tgt      []int8
	stubOK   []bool
	log      []undoOp
	cells    []int
	crossSel [][]int
	upX      []int8
	ufP      []int8
	ufR      []int8
	t        int
	s        int
	nodes    uint64
	pass     int // bucket pass kept by this enumeration (multi-pass mode)
	passes   int // total passes; ≤ 1 disables the mask filter
	lower    bool
}

// undoOp records one state mutation to roll back; kind 0 = edge applied at a
// (b is the other endpoint), 1 = union-find link of child a under parent b
// with b's rank grown when rk, 2 = crossing of lower cell a to upper cell u.
type undoOp struct {
	u    int32
	kind int8
	a    int8
	b    int8
	rk   bool
}

// newHalfEnum prepares the enumeration of one half for endpoint pair (s,t),
// keeping only profiles whose mask hashes to bucket `pass` of `passes`
// (passes ≤ 1 keeps every mask — the single-pass mode).
func newHalfEnum(g *cutGeom, ctx context.Context, lower bool, s, t, pass, passes int, table profileSink) *halfEnum {
	h := &halfEnum{g: g, ctx: ctx, lower: lower, s: s, t: t, pass: pass, passes: passes, table: table}
	if lower {
		h.cells, h.adj, h.cross = g.loCells, g.loAdj, g.loCross
	} else {
		h.cells, h.adj = g.upCells, g.upAdj
	}
	nc := len(h.cells)
	h.tgt = make([]int8, nc)
	h.stubOK = make([]bool, nc)
	h.deg = make([]int8, nc)
	h.nbrs = make([][]int8, nc)
	h.crossSel = make([][]int, nc)
	h.ufP = make([]int8, nc)
	h.ufR = make([]int8, nc)
	for i := range nc {
		h.ufP[i] = int8(i)
		h.tgt[i] = 2 // interior cells join the tour path with degree 2
		h.stubOK[i] = g.seamCell(h.cells[i]) && (lower || g.hasDownCross(h.cells[i]))
		if c := h.cells[i]; c == s || c == t {
			h.tgt[i] = 1
		}
	}
	h.upX = make([]int8, len(g.seam))
	return h
}

// run enumerates the whole half; errors surface from emit, the table's cap
// budget or ctx.
func (h *halfEnum) run() error {
	h.dfs(0)
	if h.err != nil {
		return h.err
	}
	return h.table.failure()
}

// maxDefect bounds how many seam defects (crossings at a lower cell) cell i
// may carry given its degree target and available partners.
func (h *halfEnum) maxDefect(i int) int {
	maxX := int(h.tgt[i])
	switch {
	case h.lower:
		return min(maxX, len(h.cross[i]))
	case !h.stubOK[i]:
		return 0
	}
	return maxX
}

// dfs decides the incident edges of cell i and recurses to i+1.
func (h *halfEnum) dfs(i int) {
	if h.err != nil {
		return
	}
	if serr := h.table.failure(); serr != nil {
		h.err = serr
		return
	}
	h.nodes++
	if h.nodes%tickNodes == 0 && h.ctx.Err() != nil {
		h.err = h.ctx.Err()
		return
	}
	if i == len(h.cells) {
		h.emit()
		return
	}
	d := int(h.deg[i])
	cands := h.adj[i]
	for x := range h.maxDefect(i) + 1 {
		f := int(h.tgt[i]) - x // required final internal degree
		if f < d || f-d > len(cands) {
			continue
		}
		if h.lower && x > 0 {
			h.pickCross(i, 0, x, cands, f-d)
		} else {
			h.pickEdges(i, 0, f-d, cands)
		}
	}
}

// pickCross chooses x crossing partners of cell i from index `from` on; every
// choice continues into the internal-edge selection.
func (h *halfEnum) pickCross(i, from, x int, cands []int, k int) {
	if x == 0 {
		h.pickEdges(i, 0, k, cands)
		return
	}
	for j := from; j < len(h.cross[i]); j++ {
		snap := len(h.log)
		ok := h.applyCross(int8(i), h.cross[i][j])
		if ok {
			h.pickCross(i, j+1, x-1, cands, k)
		}
		h.rollback(snap)
	}
}

// pickEdges selects exactly k internal edges among cands[from:] and recurses.
// Every branch rolls the log back to its own pre-edge snapshot, so state is
// restored on return by construction (dfs relies on this invariant).
func (h *halfEnum) pickEdges(i, from, k int, cands []int) {
	if k == 0 {
		h.dfs(i + 1)
		return
	}
	for j := from; j < len(cands); j++ {
		snap := len(h.log)
		ok := h.addEdge(int8(i), int8(cands[j]))
		if ok {
			h.pickEdges(i, j+1, k-1, cands)
		}
		h.rollback(snap)
	}
}

// addEdge applies internal edge (a,b); false when it would exceed b's degree
// target or close a premature cycle.
func (h *halfEnum) addEdge(a, b int8) bool {
	if h.deg[b] >= h.tgt[b] {
		return false
	}
	ra, rb := h.find(a), h.find(b)
	if ra == rb {
		return false
	}
	h.log = append(h.log, undoOp{kind: 0, a: a, b: b})
	h.deg[a]++
	h.deg[b]++
	h.nbrs[a] = append(h.nbrs[a], b)
	h.nbrs[b] = append(h.nbrs[b], a)
	h.union(ra, rb)
	return true
}

// applyCross records one chosen crossing edge of lower cell i to upper cell u;
// false when u would take a third crossing — an upper cell carries degree ≤ 2,
// so such configs are never paths (and the mask would overflow its 2-bit slot).
func (h *halfEnum) applyCross(i int8, u int) bool {
	if h.upX[h.g.seamIdx[u]] >= 2 {
		return false
	}
	h.log = append(h.log, undoOp{kind: 2, a: i, u: int32(u)})
	h.crossSel[i] = append(h.crossSel[i], u)
	h.upX[h.g.seamIdx[u]]++
	return true
}

// rollback unwinds the undo log to snap.
func (h *halfEnum) rollback(snap int) {
	for len(h.log) > snap {
		op := h.log[len(h.log)-1]
		h.log = h.log[:len(h.log)-1]
		switch op.kind {
		case 0:
			h.deg[op.a]--
			h.deg[op.b]--
			h.nbrs[op.a] = h.nbrs[op.a][:len(h.nbrs[op.a])-1]
			h.detach(op.b, op.a)
		case 1:
			h.ufP[op.a] = op.a
			if op.rk {
				h.ufR[op.b]--
			}
		case 2:
			last := len(h.crossSel[op.a]) - 1
			u := h.crossSel[op.a][last]
			h.crossSel[op.a] = h.crossSel[op.a][:last]
			h.upX[h.g.seamIdx[u]]--
		}
	}
}

// detach removes neighbour `from` from cell c's adjacency (degree ≤ 2).
func (h *halfEnum) detach(c, from int8) {
	nb := h.nbrs[c]
	for i, v := range nb {
		if v == from {
			h.nbrs[c] = append(nb[:i], nb[i+1:]...)
			return
		}
	}
}

// find is union-find without path compression (undoable; ≤ 32 cells per half).
func (h *halfEnum) find(a int8) int8 {
	for h.ufP[a] != a {
		a = h.ufP[a]
	}
	return a
}

// union links two roots by rank and logs the link with its rank effect.
func (h *halfEnum) union(ra, rb int8) {
	if h.ufR[ra] < h.ufR[rb] {
		ra, rb = rb, ra
	}
	grew := h.ufR[ra] == h.ufR[rb]
	h.log = append(h.log, undoOp{kind: 1, a: rb, b: ra, rk: grew})
	h.ufP[rb] = ra
	if grew {
		h.ufR[ra]++
	}
}

// emit aggregates the finished forest into the aggregation table: upper half
// keys (mask, P) from its own fragment ends, lower half derives (mask, Q) by
// routing each fragment end out through its chosen crossing edge.
func (h *halfEnum) emit() {
	xs := make([]int8, len(h.g.seam))
	if h.lower {
		copy(xs, h.upX)
	} else {
		for i, c := range h.cells {
			if d := int(h.tgt[i]) - int(h.deg[i]); d > 0 {
				xs[h.g.seamIdx[c]] = int8(d)
			}
		}
	}
	k := maskOf(xs)
	if h.passes > 1 && maskBucket(k, h.passes) != h.pass {
		return // mask lives in another pass's bucket; skip before building parings
	}
	base, m := occBase(xs)
	par := make([]int8, m+2)
	for i := range par {
		par[i] = noNode
	}
	var crossNode [][]int8
	if h.lower {
		crossNode = h.numberCrossings(base)
	}
	if !h.buildPairing(base, crossNode, par, m) {
		return
	}
	h.table.add(profKey{K: k, P: encodePairing(par)}, 1)
}

// numberCrossings assigns each chosen crossing edge its upper seam occurrence
// node: crossings are grouped by upper cell in frame order and numbered at
// each cell by ascending lower partner — a canonical slot order shared by any
// run producing the same mask.
func (h *halfEnum) numberCrossings(base []int8) [][]int8 {
	type edge struct{ f, v int8 }
	byCell := make([][]edge, len(h.g.seam))
	for i := range h.cells {
		for _, u := range h.crossSel[i] {
			f := int8(h.g.seamIdx[u])
			byCell[f] = append(byCell[f], edge{f, int8(i)})
		}
	}
	out := make([][]int8, len(h.cells))
	for _, edges := range byCell {
		slices.SortFunc(edges, func(a, b edge) int { return int(a.v) - int(b.v) })
		for slot, e := range edges {
			// A lower cell hosts at most two crossings and consumes them as an
			// unordered end set, so per-cell append order is irrelevant.
			out[e.v] = append(out[e.v], base[e.f]+int8(slot))
		}
	}
	return out
}

// buildPairing walks the forest components and pairs each component's two
// fragment ends into par (involution). Occurrence nodes come from seam defects
// routed per side (crossNode on the lower half), zero ends ⊥s at m and ⊥t at
// m+1 stand for global endpoints lying in this half. It returns false for
// impossible components — such configs cannot lie on a tour and are skipped.
func (h *halfEnum) buildPairing(base []int8, crossNode [][]int8, par []int8, m int) bool {
	seen := make([]bool, len(h.cells))
	for i := range h.cells {
		if seen[i] {
			continue
		}
		var ends []int8
		ok := true
		h.walkComponent(int8(i), seen, func(c int8) bool {
			ends = h.cellEnds(c, base, crossNode, ends, m)
			if len(ends) > 2 {
				ok = false
				return false
			}
			return true
		})
		if !ok || len(ends) != 2 || ends[0] == ends[1] ||
			par[ends[0]] != noNode || par[ends[1]] != noNode {
			return false
		}
		par[ends[0]], par[ends[1]] = ends[1], ends[0]
	}
	return true
}

// cellEnds appends the fragment-end nodes contributed by local cell c: seam
// occurrence ends (upper: defect slots; lower: routed crossings) plus a zero
// end when an endpoint of the tour sits here.
func (h *halfEnum) cellEnds(c int8, base []int8, crossNode [][]int8, ends []int8, m int) []int8 {
	if h.lower {
		ends = append(ends, crossNode[c]...)
	} else if f := h.g.seamIdx[h.cells[c]]; f >= 0 {
		for d := int(h.tgt[c]) - int(h.deg[c]); d > 0; d-- {
			ends = append(ends, base[f]+int8(d-1))
		}
	}
	if h.cells[c] == h.s {
		ends = append(ends, int8(m))
	}
	if h.cells[c] == h.t {
		ends = append(ends, int8(m+1))
	}
	return ends
}

// walkComponent traverses the path component of start via nbrs, calling visit
// per cell; iteration stops when visit returns false.
func (h *halfEnum) walkComponent(start int8, seen []bool, visit func(c int8) bool) {
	stack := []int8{start}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[c] {
			continue
		}
		seen[c] = true
		if !visit(c) {
			return
		}
		stack = append(stack, h.nbrs[c]...)
	}
}

// occBase returns the prefix-sum node bases of xs and their total: the node
// of the k-th defect at seam slot f is base[f]+k.
func occBase(xs []int8) (base []int8, m int) {
	base = make([]int8, len(xs))
	for i, v := range xs {
		base[i] = int8(m)
		m += int(v)
	}
	return base, m
}
