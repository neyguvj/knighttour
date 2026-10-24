package zdd

import (
	"fmt"
	"slices"
)

// frontier is the per-build bookkeeping of the edge order: which vertex is
// born (sees its first incident edge) and dies (sees its last one) at which
// position, and how the active boundary vertices of every level are laid out
// into state slots. A vertex is active at level i (decisions 0..i−1 made) iff
// first[v] < i ≤ last[v]; slot[i][v] gives its slot index there, −1 otherwise.
//
// n counts every endpoint id the edge list may use; nr counts the real
// vertices that carry the cover requirement. Open-seam builds set n > nr:
// ids in [nr, n) are virtual stub leaves — never active, never closing, not
// bound by coverage (whole-board Build keeps n == nr, so those loops behave
// exactly as before).
type frontier struct {
	order   []int
	eu      []int32
	ev      []int32
	width   []int
	active  [][]int32
	slot    [][]int8
	closing [][]int32
	n       int // endpoint id upper bound (real + virtual)
	nr      int // real vertices: the cover and the ≤ maxVerts encoding bound
	m       int
	empty   bool
}

// newFrontier validates the graph and precomputes the level layout. It
// enforces the Build input contract: nVerts ≥ 1 and ≤ maxVerts, endpoints in
// [0, nVerts), no loops, no duplicate edges, order a permutation of [0, m).
func newFrontier(nVerts int, edges [][2]int, order []int) (*frontier, error) {
	return newFrontierTotal(nVerts, nVerts, edges, order)
}

// newFrontierTotal is the shared constructor: nr real vertices plus virtual
// endpoint ids up to (excluding) nTotal. The encoding bound applies to nr;
// virtual leaves never enter state slots, so they may push nTotal past
// maxVerts (BuildHalf caps it at 127 for the int8 slot arrays).
func newFrontierTotal(nr, nTotal int, edges [][2]int, order []int) (*frontier, error) {
	if nr < 1 {
		return nil, fmt.Errorf("zdd: nVerts must be ≥ 1, got %d", nr)
	}
	if nr > maxVerts {
		return nil, fmt.Errorf("zdd: nVerts %d exceeds the kernel bound %d", nr, maxVerts)
	}
	if err := validateEdges(nTotal, edges); err != nil {
		return nil, err
	}
	m := len(edges)
	ord, err := normalizeOrder(order, m)
	if err != nil {
		return nil, err
	}

	f := &frontier{n: nTotal, nr: nr, m: m, order: ord, eu: make([]int32, m), ev: make([]int32, m)}
	first, last := f.fillIncidence(edges)
	f.buildLayout(first, last)
	return f, nil
}

// fillIncidence resolves the endpoints of every position and computes for each
// vertex its first and last incident position (first = m, last = −1 marks a
// vertex with no incident edges).
func (f *frontier) fillIncidence(edges [][2]int) (first, last []int32) {
	first = make([]int32, f.n)
	last = make([]int32, f.n)
	for v := range first {
		first[v] = int32(f.m) // m acts as "no incident edge yet"
		last[v] = -1
	}
	for p := range f.m {
		e := edges[f.order[p]]
		f.eu[p], f.ev[p] = int32(e[0]), int32(e[1])
		for _, x := range [2]int{e[0], e[1]} {
			if first[x] > int32(p) {
				first[x] = int32(p)
			}
			if last[x] < int32(p) {
				last[x] = int32(p)
			}
		}
	}
	return first, last
}

// buildLayout precomputes per-level active-vertex lists, the vertex→slot map
// and the closing schedule; it flags an isolated real vertex as an empty
// family. Virtual ids (≥ nr) never close and never occupy a slot.
func (f *frontier) buildLayout(first, last []int32) {
	f.width = make([]int, f.m+1)
	f.active = make([][]int32, f.m+1)
	f.slot = make([][]int8, f.m+1)
	for i := range f.m + 1 {
		f.slot[i] = make([]int8, f.n)
		for v := range f.slot[i] {
			f.slot[i][v] = -1
		}
	}
	f.closing = make([][]int32, f.m)
	for v := range f.nr {
		if last[v] < 0 {
			f.empty = true // isolated real vertex: nothing can cover it (spec edge case)
			continue
		}
		f.closing[last[v]] = append(f.closing[last[v]], int32(v))
	}
	for i := range f.m + 1 {
		for v := range f.nr {
			if first[v] < int32(i) && int32(i) <= last[v] {
				f.slot[i][v] = int8(len(f.active[i]))
				f.active[i] = append(f.active[i], int32(v))
			}
		}
		f.width[i] = len(f.active[i])
	}
}

// validateEdges rejects out-of-range endpoints, loops and duplicate edges.
// Duplicates are detected on a sorted copy of canonical (min, max) keys, so no
// map participates in the build path.
func validateEdges(nVerts int, edges [][2]int) error {
	keys := make([]uint64, 0, len(edges))
	for k, e := range edges {
		if e[0] < 0 || e[0] >= nVerts || e[1] < 0 || e[1] >= nVerts {
			return fmt.Errorf("zdd: edge %d has endpoint outside [0, %d): %v", k, nVerts, e)
		}
		if e[0] == e[1] {
			return fmt.Errorf("zdd: edge %d is a loop: %v", k, e)
		}
		lo, hi := e[0], e[1]
		if lo > hi {
			lo, hi = hi, lo
		}
		keys = append(keys, uint64(lo)*uint64(nVerts)+uint64(hi))
	}
	slices.Sort(keys)
	for i := 1; i < len(keys); i++ {
		if keys[i] == keys[i-1] {
			return fmt.Errorf("zdd: duplicate edge in the edge list")
		}
	}
	return nil
}

// normalizeOrder returns the identity permutation for a nil order and verifies
// that a non-nil order is a permutation of [0, m).
func normalizeOrder(order []int, m int) ([]int, error) {
	if order == nil {
		ord := make([]int, m)
		for i := range ord {
			ord[i] = i
		}
		return ord, nil
	}
	if len(order) != m {
		return nil, fmt.Errorf("zdd: order length %d, want %d", len(order), m)
	}
	seen := make([]bool, m)
	for k, p := range order {
		if p < 0 || p >= m || seen[p] {
			return nil, fmt.Errorf("zdd: order is not a permutation of [0, %d): bad entry %d at %d", m, p, k)
		}
		seen[p] = true
	}
	return order, nil
}

// state is the frontier configuration at a level entry: for every active slot
// the chosen degree (0..2) and component label (valid only where degree > 0,
// canonical by first appearance), plus the number of vertices that already
// closed with final degree exactly 1 (the "ends" counter; a Hamiltonian path
// needs exactly two) and, for the Cycles family, the absorbing sealed flag:
// once a same-component edge closes the cycle, no further edge may be taken.
//
// Open-seam builds additionally carry term: one byte per slot holding the
// closed-terminal mark of the slot's component (markNone, markGlobal, or a
// stub mark markStubBase+seam id — see seam.go). The emitted-pair list is NOT
// part of the state (plan 27): only the live component marks are, so two
// states with different future pair emissions never merge.
type state struct {
	deg    []uint8
	lab    []uint8
	term   []uint8 // open-seam mode only; nil in whole-board mode
	ends   uint8
	sealed bool
}

// newState returns the canonical empty state of a given width.
func newState(width int) *state {
	return &state{deg: make([]uint8, width), lab: make([]uint8, width)}
}

// newSeamState returns the canonical empty state of a given width with the
// terminal-mark array allocated (open-seam mode).
func newSeamState(width int) *state {
	return &state{deg: make([]uint8, width), lab: make([]uint8, width), term: make([]uint8, width)}
}

// packKey serializes (per-slot degrees+labels, [marks], ends|sealed) into the
// memo key: one byte per slot with degree in bits 0..2 and the component label
// above it. The active vertex set is fixed per level, so slot data alone
// identifies the state; labels are canonical (contiguous from 0). A labelled
// component owns at least two graph vertices — the endpoints of an edge — but
// only one of them still has to be active, so the bound on the label count is
// nVerts/2 ≤ 32 components, not width/2; label values stay below 32 and the
// packing never overflows. Labels are therefore comparable byte-for-byte. The
// last byte carries ends in bits 0..1 (it only ever reaches 2 before rejection)
// and the Cycles sealed flag in bit 2. The level itself is not part of the
// key: the per-level memo table it goes into fixes the frontier width and
// thereby the key length (plan 24 lever A).
//
// Open-seam states insert one terminal-mark byte per slot between the label
// bytes and the mark byte (whole-board states have no marks, so their bytes —
// and the ADR-041/042 anchors — are unchanged).
func packKey(st *state, buf []byte) []byte {
	for s := range st.deg {
		buf = append(buf, st.deg[s]|st.lab[s]<<3)
	}
	if st.term != nil {
		buf = append(buf, st.term...)
	}
	mark := st.ends
	if st.sealed {
		mark |= 1 << 2
	}
	return append(buf, mark)
}

// keySize returns the packed byte length of a state: width+1 whole-board,
// 2·width+1 with terminal marks (plan 27 budget line "2·width + 1 байт").
func keySize(st *state) int {
	if st.term != nil {
		return 2*len(st.deg) + 1
	}
	return len(st.deg) + 1
}

// unpackKeySeam is the exact inverse of packKey for open-seam keys: label bytes,
// mark bytes, ends|sealed mark byte. The replay in sigma.go feeds decoded
// states back through step, so any drift between pack and unpack breaks the σ
// pass silently — a round-trip test pins it.
func unpackKeySeam(key []byte) state {
	w := (len(key) - 1) / 2
	st := state{
		deg:  make([]uint8, w),
		lab:  make([]uint8, w),
		term: make([]uint8, w),
	}
	for s := range w {
		st.deg[s] = key[s] & 7
		st.lab[s] = key[s] >> 3
		st.term[s] = key[w+s]
	}
	st.ends = key[2*w] & 3
	return st
}
