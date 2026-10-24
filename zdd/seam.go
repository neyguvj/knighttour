package zdd

import (
	"errors"
	"fmt"
	"math/bits"
)

// Terminal marks carried per state slot in open-seam mode (plan 27). A mark
// records the closed terminal of the slot's component: none, a local global
// end, or seam stub #m−markStubBase. The emitted-pair list deliberately does
// NOT live in the state — pairs leave the build through b.emit and are summed
// only by the σ replay, which is what keeps the memo keys M-free.
const (
	markNone     = uint8(iota) // component has no closed terminal yet
	markGlobal                 // a local tour end (counted by ε)
	markStubBase               // stub #k carries mark markStubBase+k
)

// termGlobal is the σ-level terminal id of a global end (the split convention);
// stub terminals keep their seam index. Marks translate to terminals in the
// σ pass, never inside the state bytes.
const termGlobal = uint8(63)

// maxSeamTotal bounds endpoint ids (real + virtual) of one half build: the
// frontier slot arrays are int8 and −1 marks "not active".
const maxSeamTotal = 127

// maxHalfEdges bounds every global edge id: validateHalfInput rejects ids
// outside [0, maxHalfEdges), so posOfEdge and the node variable space stay
// bounded by construction. Real boards stay far below it (m ≤ 168).
const maxHalfEdges = 1 << 16

// HalfSession is one open-seam build session: a node arena, a unique table
// and a byte registry shared by every BuildHalf call, so residual
// subfunctions of different halves reuse nodes (variables are named by global
// board edge ids). The session is single-threaded like Build.
type HalfSession struct {
	a     arena
	uniq  uniqueTable
	memoB int64 // frozen memo bytes of finished builds (registry part)
	fam   Family
	// closed marks an over-cap cutoff (build or σ pass): further work is refused.
	closed bool
}

// NewHalfSession returns an empty session for family fam. The arena starts
// with the two terminals and the unique table with its initial slots; both
// live until the session is discarded.
func NewHalfSession(fam Family) *HalfSession {
	return &HalfSession{a: *newArena(), uniq: *newUniqueTable(), fam: fam}
}

// HalfDiagram is one built half-board diagram inside its session. It is
// immutable after a successful BuildHalf; Sigmas computes and caches the σ
// distribution on first call. The build machine (frontier, memo tables) is
// retained for the deterministic σ replay.
type HalfDiagram struct {
	s           *HalfSession
	b           *builder // replay machinery: frontier + memo tables + seam fields
	sigmaErr    error    // cached σ-pass cutoff (OverCapError), nil on success
	sigma       map[Sigma]uint64
	posOf       []int  // global edge id → position in this half's order
	m           int    // decision levels of this half
	nodes       int    // session nodes reachable from root (terminals included)
	levelPeak   int    // max reachable non-terminal nodes on one level
	distEntries uint64 // Σ over levels of the σ-pass intermediate map sizes;
	// published by the census as distEntries/op (specs/benchmarks.md)
	root      uint32
	sigmaOnce bool
}

// BuildHalf builds the open-seam diagram of one board half inside the
// session. edges lists internal and stub edges; stubOf[k] ≥ 0 marks edge k a
// seam stub #stubOf[k] (−1 internal) whose endpoints are exactly one real
// vertex (< nVerts) and one virtual leaf in [nVerts, nTotal); edgeIds[k] is
// the global board index of edge k — it names the node variable, so the two
// halves share nodes on common seam edges. order is a permutation of [0, m),
// nil means row order. Invalid input (unknown family, array lengths
// disagreeing with the edge list, a stub without its real end or virtual
// leaf, a doubled stub id or edgeId, an internal edge touching a virtual id,
// plus everything Build rejects) returns an error; exceeding the session
// ceiling returns an OverCapError with the session-wide counters.
func (s *HalfSession) BuildHalf(nVerts int, edges [][2]int, stubOf, edgeIds []int32, order []int) (*HalfDiagram, error) {
	if !s.fam.valid() {
		return nil, fmt.Errorf("zdd: unknown family %d", int(s.fam))
	}
	if s.closed {
		return nil, fmt.Errorf("zdd: session aborted by a previous over-cap cutoff")
	}
	nTotal, err := validateHalfInput(nVerts, edges, stubOf, edgeIds)
	if err != nil {
		return nil, err
	}
	f, err := newFrontierTotal(nVerts, nTotal, edges, order)
	if err != nil {
		return nil, err
	}

	b := &builder{f: *f, a: s.a, uniq: s.uniq, memo: *newMemoTables(f.m), fam: s.fam,
		seam: true, nReal: int32(nVerts), edgeIds: edgeIds, stubPos: stubPerPosition(stubOf, f.order)}
	b.otherBytes = func() int64 { return s.memoB }
	root := idZero
	if !f.empty { // an isolated real vertex empties the half family (spec edge case)
		root = b.build()
		if b.err != nil {
			var oc *OverCapError
			if errors.As(b.err, &oc) {
				s.closed = true
			}
			return nil, b.err
		}
	}
	hd := &HalfDiagram{s: s, b: b, root: root, m: f.m, posOf: posOfEdge(edgeIds, f.order)}
	s.a = b.a
	s.uniq = b.uniq
	s.memoB += b.memo.bytes

	hd.nodes, hd.levelPeak = halfReach(&s.a, root, hd.posOf, f.m)
	return hd, nil
}

// LiveNodes returns the number of session nodes reachable from this half's
// root; each distinct reachable terminal (0, 1 or 2 of them) counts once.
// Nodes shared with the other half count in both; their difference is the
// reuse metric.
func (h *HalfDiagram) LiveNodes() int { return h.nodes }

// LevelPeak returns the maximum number of non-terminal nodes on one level of
// this half's diagram — the measured node width of the widest cut.
func (h *HalfDiagram) LevelPeak() int { return h.levelPeak }

// registryBytes reports the session's reserved build-structure bytes (the
// same sum the cap check bounds). The census reads it as liveBytesMB/op.
func (h *HalfDiagram) registryBytes() int64 { return h.s.liveBytes() }

// liveBytes returns the session registry: shared arena and unique table plus
// the frozen memos of every finished build — the build-phase part the soft
// ceiling bounds (σ-pass structures are estimated on top by HalfDiagram.budget).
func (s *HalfSession) liveBytes() int64 {
	return int64(cap(s.a.nodes))*nodeBytes +
		int64(cap(s.uniq.slots))*uniqSlotBytes + s.memoB
}

// validateHalfInput checks the BuildHalf contract beyond newFrontierTotal and
// returns the endpoint id upper bound nTotal: array lengths, per-stub shape
// (exactly one real end plus a virtual leaf), unique stub ids and edgeIds,
// and that internal edges never touch a virtual id.
func validateHalfInput(nVerts int, edges [][2]int, stubOf, edgeIds []int32) (int, error) {
	m := len(edges)
	if len(stubOf) != m || len(edgeIds) != m {
		return 0, fmt.Errorf("zdd: stubOf/edgeIds lengths %d/%d, want %d", len(stubOf), len(edgeIds), m)
	}
	v := halfValidator{
		nVerts:     nVerts,
		nTotal:     nVerts,
		stubSeen:   make(map[int32]int, m),
		eidSeen:    make(map[int32]int, m),
		virtualUse: make(map[int]int, m),
	}
	for k, e := range edges {
		if err := v.edge(k, e, stubOf[k], edgeIds[k]); err != nil {
			return 0, err
		}
	}
	if err := v.finish(); err != nil {
		return 0, err
	}
	return v.nTotal, nil
}

// halfValidator accumulates the cross-edge state of open-seam input
// validation: id uniqueness, seam-stub uniqueness and virtual-leaf usage.
type halfValidator struct {
	stubSeen   map[int32]int // seam stub id → first edge carrying it
	eidSeen    map[int32]int // global edge id → first edge carrying it
	virtualUse map[int]int   // virtual endpoint id → incident edge count
	nVerts     int
	nTotal     int
}

// edge validates one edge: endpoints, global-id uniqueness, virtual usage
// and the declared stub/internal shape.
func (v *halfValidator) edge(k int, e [2]int, s, id int32) error {
	if e[0] < 0 || e[1] < 0 {
		return fmt.Errorf("zdd: edge %d has a negative endpoint: %v", k, e)
	}
	if id < 0 || id >= maxHalfEdges {
		return fmt.Errorf("zdd: edgeIds[%d] = %d outside [0, %d)", k, id, maxHalfEdges)
	}
	if dup, seen := v.eidSeen[id]; seen {
		return fmt.Errorf("zdd: edgeIds repeat %d at edges %d and %d", id, dup, k)
	}
	v.eidSeen[id] = k
	v.nTotal = max(v.nTotal, max(e[0], e[1])+1)
	realEnds := 0
	for _, x := range e {
		if x >= v.nVerts {
			v.virtualUse[x]++
		} else {
			realEnds++
		}
	}
	return v.shape(k, e, s, realEnds)
}

// shape checks one edge's declared stub/internal form against its endpoint
// counts: a stub has exactly one real end and a fresh seam id, an internal
// edge only real ends.
func (v *halfValidator) shape(k int, e [2]int, s int32, realEnds int) error {
	switch {
	case s >= 0:
		if dup, seen := v.stubSeen[s]; seen {
			return fmt.Errorf("zdd: doubled seam stub id %d at edges %d and %d", s, dup, k)
		}
		v.stubSeen[s] = k
		if s >= int32(termGlobal-markStubBase) {
			return fmt.Errorf("zdd: seam stub id %d out of terminal range", s)
		}
		if realEnds != 1 {
			return fmt.Errorf("zdd: stub edge %d needs exactly one real end: %v", k, e)
		}
	case s == -1:
		if realEnds != 2 {
			return fmt.Errorf("zdd: internal edge %d touches a virtual vertex: %v", k, e)
		}
	default:
		return fmt.Errorf("zdd: stubOf[%d] = %d, want −1 or a seam index", k, s)
	}
	return nil
}

// finish checks that every virtual id is a leaf and the id bounds hold.
func (v *halfValidator) finish() error {
	for x, c := range v.virtualUse {
		if c != 1 {
			return fmt.Errorf("zdd: virtual vertex %d has %d incident edges, stubs need leaves", x, c)
		}
	}
	if v.nTotal > maxSeamTotal {
		return fmt.Errorf("zdd: half uses %d endpoint ids, the seam bound is %d", v.nTotal, maxSeamTotal)
	}
	return nil
}

// stubPerPosition maps each order position to its seam stub id (−1 internal).
func stubPerPosition(stubOf []int32, order []int) []int32 {
	pos := make([]int32, len(order))
	for p, k := range order {
		pos[p] = stubOf[k]
	}
	return pos
}

// posOfEdge maps a global edge id to its position in the half's order.
func posOfEdge(edgeIds []int32, order []int) []int {
	maxID := int32(0)
	for _, id := range edgeIds {
		maxID = max(maxID, id)
	}
	pos := make([]int, maxID+1)
	for p, k := range order {
		pos[edgeIds[k]] = p
	}
	return pos
}

// halfReach counts the nodes reachable from root (terminals included) and the
// widest level; seen is a one-shot bitmap over the arena.
func halfReach(a *arena, root uint32, posOf []int, m int) (nodes, peak int) {
	seen := make([]bool, len(a.nodes))
	stack := []uint32{root}
	counts := make([]int, m)
	var termBits uint8 // distinct reachable terminals: bit idZero / idOne (0..2 set)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id < numTerminals {
			termBits |= 1 << id
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		nodes++
		n := &a.nodes[id]
		counts[posOf[n.variable]]++
		peak = max(peak, counts[posOf[n.variable]])
		stack = append(stack, n.low, n.high)
	}
	return nodes + int(bits.OnesCount8(termBits)), peak
}

// seamAccepts is the terminal-level criterion of the half family: cover and
// fragment pairing were enforced on every transition; what remains to bound
// at the empty frontier is ε ≤ 2 for Paths (Cycles never see a global end).
func seamAccepts(fam Family, st *state) bool {
	if fam == Cycles {
		return true
	}
	return st.ends <= 2
}

// emitPair records one interface pair emitted by the current transition;
// marks travel untranslated (the σ pass maps them to terminals). A
// transition emits at most one pair per component present (≤ 32 components),
// which bounds nEmit well inside emitBuf.
func (b *builder) emitPair(t1, t2 uint8) {
	b.emit[b.nEmit] = [2]uint8{t1, t2}
	b.nEmit++
}

// stepSeam applies one seam decision at position i and normalizes the child:
// stub takes attach the stub terminal to the real end's component; internal
// decisions reuse the whole-board machinery, then carry vanishing marks, unify
// marks, run the cover pass and close degree-1 ends as global terminals.
// Emissions land in b.emit for the σ replay; the build itself ignores them.
func (b *builder) stepSeam(i int, st *state, take bool) (*state, bool) {
	b.nEmit = 0
	b.pended = false
	if stub := b.stubPos[i]; stub >= 0 && take {
		return b.stepStub(i, st, uint8(stub)+markStubBase)
	}
	f := &b.f
	d, ok := newDecision(st, f.slot[i][f.eu[i]], f.slot[i][f.ev[i]], take, b.fam)
	if !ok || d.seals {
		return nil, false // a seam half never seals the cycle inside itself
	}
	child := b.apply(i, st, take, d)
	if take {
		touched := labelAfter(&d, st)
		if !b.carryVanishing(i, st, child, &d, touched, true) {
			return nil, false
		}
		if !b.unifyMarks(st, &d, child) {
			return nil, false
		}
	} else if !b.carryVanishing(i, st, child, &d, noneLabel, false) {
		return nil, false
	}
	if !b.closingSeam(i, st, child, take, &d, noFresh, 0) {
		return nil, false
	}
	if b.pended || !invariantSeam(child) {
		return nil, false
	}
	canonicalizeSeam(child)
	return child, true
}

// noFresh marks "no component is born at this step" for the closing passes.
const noFresh = int32(-1)

// noneLabel is a label no component carries (labels stay below 32), marking
// "no touched component" for the carry pass.
const noneLabel = uint8(255)

// carryVanishing preserves the terminal mark of components that leave the
// frontier at this position: closing vertices not owned by the take (not an
// endpoint, not in the taken component) hand their parent-state mark to the
// pending slot before it disappears with the slot. Components touched by the
// take are handled by unifyMarks/stepStub from the endpoint slots instead —
// carrying them twice would duplicate a terminal.
func (b *builder) carryVanishing(i int, st, child *state, d *decision, touched uint8, take bool) bool {
	f := &b.f
	u, v := f.eu[i], f.ev[i]
	for _, x := range f.closing[i] {
		if x >= b.nReal || (take && (x == u || x == v)) {
			continue // a taken endpoint's mark is relocate-by-unifyMarks' job
		}
		pm := b.parentMark(i, st, x)
		if pm == markNone {
			continue // nothing to preserve: a fresh or unmarked component
		}
		l := b.labelOfClosing(i, st, x, d, noFresh, 0)
		if l == touched || labelLive(child, l) || (b.pended && b.pendLab == l) {
			continue
		}
		if !b.putTerminal(child, l, pm) {
			return false
		}
	}
	return true
}

// unifyMarks restores per-component mark uniformity after an internal take:
// a join or merge must move the surviving mark onto every slot of the
// component, and merging two marked fragments completes one — its pair is
// emitted and the marks cleared. A result with no live slot parks its mark.
func (b *builder) unifyMarks(st *state, d *decision, child *state) bool {
	var tmU, tmV uint8
	if d.du > 0 {
		tmU = st.term[d.su]
	}
	if d.dv > 0 {
		tmV = st.term[d.sv]
	}
	l := labelAfter(d, st)
	switch {
	case tmU != markNone && tmV != markNone:
		b.emitPair(tmU, tmV) // two closed terminals joined: the fragment completed
		clearMark(child, l)
		return true
	case max(tmU, tmV) == markNone:
		return true
	default:
		return b.putMark(child, l, max(tmU, tmV))
	}
}

// labelAfter returns the component label of a take in the child state: the
// fresh label for a new component, else the mapped label of a non-fresh
// endpoint (a merge maps both onto one).
func labelAfter(d *decision, st *state) uint8 {
	if d.fresh {
		return d.freshLabel
	}
	if d.du > 0 {
		return d.mapLabel(st.lab[d.su])
	}
	return d.mapLabel(st.lab[d.sv])
}

// putMark propagates the existing terminal tm of the taken component onto its
// child slots; with no live slot left it parks instead. This is relocation,
// not a new terminal — no pair logic here.
func (b *builder) putMark(child *state, l, tm uint8) bool {
	if labelLive(child, l) {
		setMark(child, l, tm)
		return true
	}
	return b.park(l, tm)
}

// park stores a dangling terminal for the vanishing component of label l.
func (b *builder) park(l, tm uint8) bool {
	if b.pended && b.pendLab != l {
		return false // two single-terminal fragments vanished at one step: invalid
	}
	b.pended, b.pendLab, b.pendMark = true, l, tm
	return true
}

// putTerminal closes a new terminal tm onto the component of label l: a live
// unmarked component takes it; a marked one completes and its pair is emitted;
// with no live slot the mark parks, and a pending twin arriving emits.
func (b *builder) putTerminal(child *state, l, tm uint8) bool {
	if b.pended && b.pendLab == l {
		b.emitPair(b.pendMark, tm)
		clearMark(child, l)
		b.pended = false
		return true
	}
	cur, found := scanMark(child, l)
	switch {
	case !found:
		return b.park(l, tm)
	case cur == markNone:
		setMark(child, l, tm)
	default:
		b.emitPair(cur, tm)
		clearMark(child, l)
	}
	return true
}

// scanMark returns the uniform terminal mark of label l's live slots (found
// is false when no slot carries the label).
func scanMark(st *state, l uint8) (uint8, bool) {
	found := false
	var cur uint8
	for s := range st.deg {
		if st.deg[s] > 0 && st.lab[s] == l {
			found = true
			if st.term[s] != markNone {
				cur = st.term[s]
			}
		}
	}
	return cur, found
}

// setMark writes tm onto every live slot of label l (mark uniformity).
func setMark(st *state, l, tm uint8) {
	for s := range st.deg {
		if st.deg[s] > 0 && st.lab[s] == l {
			st.term[s] = tm
		}
	}
}

// clearMark zeroes the marks of label l after its pair was emitted; the
// component freezes — all its live slots sit at degree 2, enforced by
// invariantSeam.
func clearMark(st *state, l uint8) { setMark(st, l, markNone) }

// stepStub takes a seam stub edge: the real endpoint's component gains the
// stub terminal (a fresh endpoint starts the component marked; a component
// that already carries one completes with the pair emission). When the real
// end leaves the frontier here and no live slot holds the component, its
// parent mark is carried into the pending slot first so the stub can pair
// against it.
func (b *builder) stepStub(i int, st *state, mark uint8) (*state, bool) {
	f := &b.f
	r := f.eu[i]
	if r >= b.nReal {
		r = f.ev[i]
	}
	sr := f.slot[i][r]
	rd := degAt(st, sr)
	if rd == 2 {
		return nil, false
	}

	child := b.copySeamSlots(i, st)
	l := stubLabel(st, sr, rd)
	if !b.stubAttach(i, st, child, r, sr, rd, l, mark) {
		return nil, false
	}

	var d decision // identity: a stub take never merges components
	d.mergeFrom = noMerge
	freshR := noFresh
	if rd == 0 {
		freshR = r // the fresh component's label is l (the stub endpoint)
	}
	if !b.carryVanishing(i, st, child, &d, l, true) {
		return nil, false
	}
	if !b.closingSeam(i, st, child, true, &d, freshR, l) {
		return nil, false
	}
	if b.pended || !invariantSeam(child) {
		return nil, false
	}
	canonicalizeSeam(child)
	return child, true
}

// copySeamSlots materializes the level-(i+1) state for a stub step: active
// slots and ends carry over unchanged (a stub take merges no components).
func (b *builder) copySeamSlots(i int, st *state) *state {
	child := b.stateAt(i + 1)
	clear(child.deg)
	clear(child.lab)
	clear(child.term)
	child.ends = st.ends
	for s, x := range b.f.active[i+1] {
		if so := b.f.slot[i][x]; so >= 0 {
			child.deg[s], child.lab[s], child.term[s] = st.deg[so], st.lab[so], st.term[so]
		}
	}
	return child
}

// stubLabel returns the component label of a stub take: the real endpoint's
// existing label, or a fresh one when it had no live edge.
func stubLabel(st *state, sr int8, rd uint8) uint8 {
	if rd > 0 {
		return st.lab[sr]
	}
	return uint8(bits.OnesCount64(labelsMask(st)))
}

// stubAttach applies the stub take to its real endpoint r: staying live grows
// the degree and pairs the stub terminal against an existing one, while a
// closing endpoint leaves its slot mark relocated (carried) and goes pending.
func (b *builder) stubAttach(i int, st, child *state, r int32, sr int8, rd, l, mark uint8) bool {
	if cs := b.f.slot[i+1][r]; cs >= 0 {
		child.deg[cs] = rd + 1 // uint8 arithmetic stays below the degree bound of 3
		child.lab[cs] = l
		if rd == 0 {
			child.term[cs] = mark
		} else if !b.putTerminal(child, l, mark) {
			return false
		}
		return true
	}
	if rd > 0 && st.term[sr] != markNone && !labelLive(child, l) && !b.putMark(child, l, st.term[sr]) {
		return false // relocate the leaving slot's mark before pairing
	}
	if !b.putTerminal(child, l, mark) {
		return false // the end closes here: the stub terminal goes pending
	}
	return true
}

// closingSeam runs the seam exit rules: pass 1 is cover — degree ≥ 1 (exactly
// 2 for Cycles), never above 2; pass 2 closes degree-1 ends as global
// terminals (ε-bounded). A pending terminal left over afterwards means a
// fragment ended with one terminal and rejects the step.
func (b *builder) closingSeam(i int, st, child *state, take bool, d *decision, freshR int32, freshL uint8) bool {
	if !b.coverPass(i, st, take) {
		return false
	}
	return b.endsPass(i, st, child, take, d, freshR, freshL)
}

// finalDeg returns the degree closing vertex x ends with at position i.
func (b *builder) finalDeg(i int, st *state, x int32, take bool) uint8 {
	fd := degAt(st, b.f.slot[i][x])
	if take && (x == b.f.eu[i] || x == b.f.ev[i]) {
		fd++
	}
	return fd
}

// coverPass rejects closers that leave a real vertex uncovered or overfull;
// Cycles demand strict cover (degree exactly 2).
func (b *builder) coverPass(i int, st *state, take bool) bool {
	for _, x := range b.f.closing[i] {
		if x >= b.nReal {
			continue // virtual leaf: exempt from cover (plan 27 design 1)
		}
		fd := b.finalDeg(i, st, x, take)
		switch {
		case fd == 0, fd > 2:
			return false
		case fd == 1 && b.fam != Paths:
			return false
		}
	}
	return true
}

// endsPass closes degree-1 real ends as global terminals (ε ≤ 2).
func (b *builder) endsPass(i int, st, child *state, take bool, d *decision, freshR int32, freshL uint8) bool {
	for _, x := range b.f.closing[i] {
		if x >= b.nReal || b.finalDeg(i, st, x, take) != 1 {
			continue
		}
		child.ends++
		if child.ends > 2 {
			return false
		}
		if !b.putTerminal(child, b.labelOfClosing(i, st, x, d, freshR, freshL), markGlobal) {
			return false
		}
	}
	return true
}

// labelOfClosing returns the post-decision component label of closing vertex
// x: its mapped parent label, or — for an endpoint born (or degree-0) at
// this position — the label the take joins it into. A closer that is not an
// endpoint of a taken edge never reaches the join branch (fd would be 0).
func (b *builder) labelOfClosing(i int, st *state, x int32, d *decision, freshR int32, freshL uint8) uint8 {
	if sx := b.f.slot[i][x]; sx >= 0 && st.deg[sx] > 0 {
		return d.mapLabel(st.lab[sx])
	}
	if x == freshR {
		return freshL // the stub take's fresh component: joinLabel does not apply
	}
	return d.joinLabel(st)
}

// parentMark reads the terminal mark of vertex x's component in the parent
// state (markNone for a component fresh at this step).
func (b *builder) parentMark(i int, st *state, x int32) uint8 {
	if sx := b.f.slot[i][x]; sx >= 0 && st.deg[sx] > 0 {
		return st.term[sx]
	}
	return markNone
}

// labelLive reports whether any live slot carries label l.
func labelLive(st *state, l uint8) bool {
	for s := range st.deg {
		if st.deg[s] > 0 && st.lab[s] == l {
			return true
		}
	}
	return false
}

// invariantSeam checks the fragment invariant of the half family on a child
// state: every component is a path whose open ends plus closed terminals
// equal two — a marked component has exactly one live end, an unmarked one
// has two (or zero when it is a frozen completed fragment); ε ≤ 2 and no
// pending terminal may survive the step.
func invariantSeam(st *state) bool {
	if st.ends > 2 {
		return false
	}
	for s := range st.deg {
		if st.deg[s] == 0 {
			continue
		}
		if !componentOK(st, st.lab[s]) {
			return false
		}
	}
	return true
}

// componentOK checks one component's fragment invariant: a marked component
// exposes exactly one live free end, an unmarked one zero or two, and marks
// inside a component must be uniform (the key contract).
func componentOK(st *state, l uint8) bool {
	ends, marked := 0, false
	var term uint8
	for t := range st.deg {
		if st.deg[t] == 0 || st.lab[t] != l {
			continue
		}
		ends += int(2 - st.deg[t])
		if tm := st.term[t]; tm != markNone {
			if marked && tm != term {
				return false
			}
			marked, term = true, tm
		}
	}
	return (marked && ends == 1) || (!marked && (ends == 0 || ends == 2))
}

// canonicalizeSeam is canonicalize extended with the mark array: labels are
// renumbered by first appearance and their marks move along; degree-0 slots
// zero both so equal states pack to identical key bytes.
func canonicalizeSeam(st *state) {
	var rel [maxVerts]int8
	var mv [maxVerts]uint8
	for l := range rel {
		rel[l] = -1
	}
	var next uint8
	for s := range st.deg {
		if st.deg[s] == 0 {
			st.lab[s], st.term[s] = 0, markNone
			continue
		}
		if rel[st.lab[s]] < 0 {
			rel[st.lab[s]] = int8(next)
			mv[next] = st.term[s]
			next++
		}
		st.lab[s] = uint8(rel[st.lab[s]])
		st.term[s] = mv[st.lab[s]]
	}
}
