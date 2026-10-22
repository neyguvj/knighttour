package zdd

import "math/bits"

// noMerge marks an inactive merge in decision; real labels are < 32.
const noMerge = uint8(255)

// decision is the outcome of one variable test at position i: which edge was
// taken or skipped, how component labels must be merged on the way into level
// i+1, and (when the edge started a new component) which provisional label the
// joined endpoints carry until canonicalization.
type decision struct {
	su, sv     int8 // slots of the endpoints at level i; −1 when born at this step
	du, dv     uint8
	mergeFrom  uint8 // noMerge, or the losing side of a component merge
	mergeTo    uint8
	fresh      bool  // the taken edge joined two degree-0 endpoints: new component
	seals      bool  // Cycles only: the edge closed the single cycle (same component)
	freshLabel uint8 // distinct from every label present in the parent state
}

// mapLabel applies the merge (if any) to a parent-state label.
func (d *decision) mapLabel(l uint8) uint8 {
	if l == d.mergeFrom {
		return d.mergeTo
	}
	return l
}

// joinLabel is the component label a degree-0 endpoint inherits when the edge
// is taken; it must only be called for such an endpoint.
func (d *decision) joinLabel(st *state) uint8 {
	if d.fresh {
		return d.freshLabel
	}
	if d.du > 0 {
		return d.mapLabel(st.lab[d.su])
	}
	return d.mapLabel(st.lab[d.sv])
}

// degAt reads the degree of a slot; a negative slot (vertex born at this very
// position) has degree 0.
func degAt(st *state, slot int8) uint8 {
	if slot < 0 {
		return 0
	}
	return st.deg[slot]
}

// labelsMask returns the set of component labels present in st as a bitmask
// (labels are < maxVerts ≤ 64).
func labelsMask(st *state) uint64 {
	var m uint64
	for s := range st.deg {
		if st.deg[s] > 0 {
			m |= 1 << st.lab[s]
		}
	}
	return m
}

// newDecision checks the take constraints of family fam — degree cap ≤ 2;
// Paths also forbids merging a vertex with its own component (a premature
// cycle), while Cycles allow exactly that take as the sealing edge (the degree
// cap already guarantees both endpoints reach degree 2) and rejects every take
// once the state is sealed. Skipping always passes.
func newDecision(st *state, su, sv int8, take bool, fam Family) (decision, bool) {
	d := decision{su: su, sv: sv, du: degAt(st, su), dv: degAt(st, sv), mergeFrom: noMerge}
	if !take {
		return d, true
	}
	if st.sealed {
		return d, false // Cycles: the sealed cycle is the whole member; no more edges
	}
	if d.du == 2 || d.dv == 2 {
		return d, false
	}
	switch {
	case d.du > 0 && d.dv > 0:
		if st.lab[d.su] == st.lab[d.sv] {
			if fam != Cycles {
				return d, false // Paths: taking the edge would close a premature cycle
			}
			d.seals = true // Cycles: both endpoints reach degree 2 — the final closure
			break
		}
		d.mergeFrom, d.mergeTo = st.lab[d.su], st.lab[d.sv]
		if d.mergeFrom > d.mergeTo {
			d.mergeFrom, d.mergeTo = d.mergeTo, d.mergeFrom
		}
	case d.du == 0 && d.dv == 0:
		d.fresh = true
		d.freshLabel = uint8(bits.OnesCount64(labelsMask(st)))
	}
	return d, true
}

// builder drives the top-down construction: frontier layout, node arena and
// the two tables (unique nodes, memoized states). Build is single-threaded,
// so per-level scratch buffers are safe: frame i owns slot i, its children
// write slot i+1, and a deeper frame never touches slots below its own.
type builder struct {
	err    error
	a      arena
	stBuf  []*state
	keyBuf [][]byte
	uniq   uniqueTable
	f      frontier
	memo   memoTables
	fam    Family
}

// build runs the construction from level 0 and returns the root node id.
func (b *builder) build() uint32 {
	b.stBuf = append(b.stBuf, newState(b.f.width[0]))
	return b.dfs(0, b.stBuf[0])
}

// stateAt returns the scratch state of a level, grown to that level's width.
// The caller clears it before filling.
func (b *builder) stateAt(level int) *state {
	for len(b.stBuf) <= level {
		b.stBuf = append(b.stBuf, nil)
	}
	s := b.stBuf[level]
	if s == nil || cap(s.deg) < b.f.width[level] {
		s = newState(b.f.width[level])
		b.stBuf[level] = s
	}
	s.deg = s.deg[:b.f.width[level]]
	s.lab = s.lab[:b.f.width[level]]
	return s
}

// keyAt returns a cleared scratch key buffer for a level, sized for width
// slot bytes plus the mark byte.
func (b *builder) keyAt(level, width int) []byte {
	for len(b.keyBuf) <= level {
		b.keyBuf = append(b.keyBuf, nil)
	}
	k := b.keyBuf[level]
	if cap(k) < 1+width {
		k = make([]byte, 0, 1+width)
	}
	k = k[:0]
	b.keyBuf[level] = k
	return k
}

// dfs returns the ZDD node of the residual subfunction at level i for state st
// (already normalized and canonical). The terminal level accepts a state iff
// the family criterion holds (Family.acceptsTerminal): Paths — exactly two
// degree-1 closures; Cycles — the single cycle sealed. Once the summed byte
// ceiling is crossed, b.err is set and every frame unwinds immediately.
func (b *builder) dfs(i int, st *state) uint32 {
	if b.err != nil {
		return idZero
	}
	if i == b.f.m {
		if b.fam.acceptsTerminal(st) {
			return idOne
		}
		return idZero
	}
	key := packKey(st, b.keyAt(i, len(st.deg)))
	if id, ok := b.memo.get(i, key); ok {
		return id
	}
	high := idZero
	if child, ok := b.step(i, st, true); ok {
		high = b.dfs(i+1, child)
	}
	low := idZero
	if child, ok := b.step(i, st, false); ok {
		low = b.dfs(i+1, child)
	}
	id := b.uniq.findOrAdd(&b.a, uint32(i), low, high)
	b.memo.put(i, key, id)
	if over := b.overCap(); over != nil {
		b.err = over
	}
	return id
}

// step applies the decision at position i and normalizes the result into the
// slot layout of level i+1. It rejects decisions that break the family: cover
// or ends violations in validateClosing, and for Paths a component losing its
// last boundary vertex before the final position (such a component can never
// reconnect, so no continuation survives). Cycles skip the sealed-component
// check: with strict cover a silently sealing component is already a cycle
// carrying the sealed flag, and every other shape dies at validateClosing.
func (b *builder) step(i int, st *state, take bool) (*state, bool) {
	f := &b.f
	d, ok := newDecision(st, f.slot[i][f.eu[i]], f.slot[i][f.ev[i]], take, b.fam)
	if !ok {
		return nil, false
	}
	child := b.apply(i, st, take, d)
	if child == nil {
		return nil, false
	}
	if b.fam == Paths && i < f.m-1 && componentClosed(st, child, &d) {
		return nil, false
	}
	canonicalize(child)
	return child, true
}

// apply copies st into the scratch state of level i+1 under decision d and
// validates the vertices that close at position i. It returns nil on a cover
// or ends violation. The sealed flag is absorbing: it survives skips and is
// set once by the sealing take (Cycles only).
func (b *builder) apply(i int, st *state, take bool, d decision) *state {
	child := b.stateAt(i + 1)
	clear(child.deg)
	clear(child.lab)
	child.ends = st.ends
	child.sealed = st.sealed || d.seals
	b.copySlots(i, st, take, d, child)
	if !b.validateClosing(i, st, take, child) {
		return nil
	}
	return child
}

// copySlots fills the level-(i+1) child from st under decision d, mapping
// component labels through the merge and attaching degree-0 endpoints of a
// taken edge to their new or inherited component.
func (b *builder) copySlots(i int, st *state, take bool, d decision, child *state) {
	u, v := b.f.eu[i], b.f.ev[i]
	for s, x := range b.f.active[i+1] {
		if so := b.f.slot[i][x]; so >= 0 {
			child.deg[s], child.lab[s] = st.deg[so], d.mapLabel(st.lab[so])
		}
		if take && (x == u || x == v) {
			child.deg[s]++
			switch {
			case d.du == 0 && d.dv == 0:
				child.lab[s] = d.freshLabel
			case degAt(st, b.f.slot[i][x]) == 0:
				child.lab[s] = d.joinLabel(st)
			}
		}
	}
}

// validateClosing checks the vertices whose last incident edge sits at
// position i. Paths: none may leave uncovered, and degree-1 closures increment
// the ends counter (a path has exactly two). Cycles use strict cover: every
// vertex must close with degree exactly 2. It updates child.ends in place.
func (b *builder) validateClosing(i int, st *state, take bool, child *state) bool {
	u, v := b.f.eu[i], b.f.ev[i]
	for _, x := range b.f.closing[i] {
		fd := degAt(st, b.f.slot[i][x])
		if take && (x == u || x == v) {
			fd++
		}
		if b.fam == Cycles {
			if fd != 2 {
				return false // strict cover: a cycle vertex closes with degree exactly 2
			}
			continue
		}
		if fd == 0 {
			return false // vertex left the frontier uncovered
		}
		if fd == 1 {
			child.ends++
			if child.ends > 2 {
				return false
			}
		}
	}
	return true
}

// componentClosed reports whether some component present before the step has
// no boundary vertex left in child (so it sealed at this position).
func componentClosed(st, child *state, d *decision) bool {
	oldMask := labelsMask(st)
	if d.mergeFrom != noMerge && oldMask&(1<<d.mergeFrom) != 0 {
		oldMask &^= 1 << d.mergeFrom
		oldMask |= 1 << d.mergeTo
	}
	childMask := labelsMask(child)
	if oldMask&^childMask != 0 {
		return true
	}
	return d.fresh && childMask&(1<<d.freshLabel) == 0
}

// canonicalize relabels components in order of first appearance (contiguous
// from 0) and zeroes the label bytes of degree-0 slots, so equal states pack
// to identical key bytes.
func canonicalize(st *state) {
	var rel [maxVerts]int8
	for l := range rel {
		rel[l] = -1
	}
	var next uint8
	for s := range st.deg {
		if st.deg[s] == 0 {
			st.lab[s] = 0
			continue
		}
		if rel[st.lab[s]] < 0 {
			rel[st.lab[s]] = int8(next)
			next++
		}
		st.lab[s] = uint8(rel[st.lab[s]])
	}
}
