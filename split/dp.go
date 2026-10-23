package split

import "errors"

// engine holds the mutable state of one count run: the two live DP generations,
// the retained σ table of the first half while the second is built, and the
// census accumulators. Nothing is shared between runs.
type engine struct {
	budget    *memBudget
	cur       *table // current generation
	next      *table // generation under construction
	retained  *table // σ table of a finished half, live through the next build
	kl        keyLayout
	fam       Family
	maxWeight uint64
	layerPeak int
}

// newEngine builds the run state with an empty budget tied to the global lever.
func newEngine(kl *keyLayout, fam Family) (*engine, error) {
	e := &engine{kl: *kl, fam: fam, budget: &memBudget{}}
	var err error
	if e.cur, err = newTable(e.budget, kl.kw); err != nil {
		return nil, e.wrapCap(err, 0)
	}
	if e.next, err = newTable(e.budget, kl.kw); err != nil {
		return nil, e.wrapCap(err, 0)
	}
	return e, nil
}

// runHalf walks one half of the board layer by layer and returns the finished
// generation: its keys are the interface states σ of that half (all window slots
// empty; ε and the emitted pair list carry over). The table stays owned by the
// engine and is accounted against the budget until the count ends.
func (e *engine) runHalf(walk *halfWalk) (*table, error) {
	if err := e.resetForHalf(); err != nil {
		return nil, e.wrapCap(err, 0)
	}
	for s := range walk.steps {
		if err := e.next.reset(e.budget, e.cur.n); err != nil {
			return nil, e.wrapCap(err, s)
		}
		e.step(walk, s)
		if e.next.err != nil {
			return nil, e.wrapCap(e.next.err, s)
		}
		e.cur, e.next = e.next, e.cur
		e.layerPeak = max(e.layerPeak, e.cur.n)
		e.maxWeight = max(e.maxWeight, e.cur.maxVal)
	}
	return e.cur, nil
}

// resetForHalf empties the working generation and seeds it with the single
// all-empty profile of weight 1 (no cell processed, no stub, no pair).
func (e *engine) resetForHalf() error {
	if err := e.cur.reset(e.budget, 1); err != nil {
		return err
	}
	var kb [maxKeyWords]uint64
	w := kb[:e.kl.kw]
	for i := range e.kl.nSlots {
		e.kl.clearSlot(w, i)
	}
	e.cur.add(e.budget, w, 1)
	return e.cur.err
}

// retain hands the finished σ table of a half to the engine (it stays budget-
// accounted and live through the next build) and restores two distinct working
// generations: the idle one is recycled and a fresh table takes its place. The
// third live table is the design point of the straight variant (σ_L + two gens).
func (e *engine) retain(t *table) error {
	// After runHalf the final table is the current generation; the other one is idle.
	e.cur = e.next
	e.retained = t
	nt, err := newTable(e.budget, e.kl.kw)
	if err != nil {
		return e.wrapCap(err, 0)
	}
	e.next = nt
	return nil
}

// step expands every live state of the current generation into the next one.
func (e *engine) step(walk *halfWalk, s int) {
	stride := e.cur.stride
	for i := 0; i < len(e.cur.buf); i += stride {
		if e.next.err != nil { // refused budget: abort the tail instead of expanding into a dead table
			return
		}
		if w := e.cur.slotWeight(i); w != 0 {
			e.expand(walk, e.cur.buf[i:i+e.kl.kw], w, s)
		}
	}
}

// liveStates counts the entries of every table the engine currently keeps.
func (e *engine) liveStates() int {
	n := e.cur.n + e.next.n
	if e.retained != nil {
		n += e.retained.n
	}
	return n
}

// wrapCap turns a refused budget growth into the contract error, filling in the
// progress counters of the moment; any other error passes through unchanged.
func (e *engine) wrapCap(err error, layer int) error {
	var ch *capHit
	if errors.As(err, &ch) {
		return &OverCapError{Layer: layer, States: e.liveStates(), Bytes: int(ch.need), Cap: softCap}
	}
	return err
}

// expand enumerates every edge subset of size ≤ 2 from the current cell — to
// already-processed internal neighbours and to seam stubs — and commits each
// legal one. Items are encoded as non-negative window slots or stubItem(j) for
// the j-th seam edge of the step; itemAbsent marks an unused subset position.
func (e *engine) expand(walk *halfWalk, k []uint64, wt uint64, s int) {
	nbrs, sts := walk.nbrs[s], walk.stubs[s]
	e.trySubset(walk, k, wt, s, itemAbsent, itemAbsent)
	for _, a := range nbrs {
		e.trySubset(walk, k, wt, s, a, itemAbsent)
	}
	for j := range sts {
		e.trySubset(walk, k, wt, s, stubItem(j), itemAbsent)
	}
	for i, a := range nbrs {
		for _, b := range nbrs[i+1:] {
			e.trySubset(walk, k, wt, s, a, b)
		}
		for j := range sts {
			e.trySubset(walk, k, wt, s, a, stubItem(j))
		}
	}
	for i := range sts {
		for j := i + 1; j < len(sts); j++ {
			e.trySubset(walk, k, wt, s, stubItem(i), stubItem(j))
		}
	}
}

// trySubset applies one edge subset to a copy of k, runs the exit rules of the
// step and commits the result when every invariant survives.
func (e *engine) trySubset(walk *halfWalk, k []uint64, wt uint64, s, a, b int) {
	kl := &e.kl
	var kb [maxKeyWords]uint64
	w := kb[:kl.kw]
	copy(w, k)
	cur := walk.curSlot[s]
	kl.clearSlot(w, cur)
	kl.setDeg(w, cur, 0)
	kl.setLabel(w, cur, kl.maxLabel(w)+1)
	if a != itemAbsent && !e.applyItem(w, s, walk, cur, a) {
		return
	}
	if b != itemAbsent && !e.applyItem(w, s, walk, cur, b) {
		return
	}
	for _, u := range walk.drops[s] {
		if !e.applyDrop(w, u) {
			return
		}
	}
	if !e.normalize(w) {
		return
	}
	e.next.add(e.budget, w, wt)
}

// itemAbsent marks an unused subset position; stub items are numbered from -2
// downward so they never collide with window slots (≥ 0) or the absent marker.
const itemAbsent = -1

// stubItem encodes the j-th seam edge of a step as a subset item.
func stubItem(j int) int { return -j - 2 }

// applyItem commits one chosen edge: an internal join at slot a ≥ 0 or a seam
// stub recorded on the current cell for item a < itemAbsent.
func (e *engine) applyItem(w []uint64, s int, walk *halfWalk, cur, a int) bool {
	if a >= 0 {
		return e.joinFragments(w, cur, a)
	}
	return e.addStub(w, cur, uint64(walk.stubs[s][-a-2]))
}

// addStub records a chosen seam edge on the current cell; its terminal id is the
// global seam index, so no per-half numbering can collide at gluing.
func (e *engine) addStub(w []uint64, cur int, stub uint64) bool {
	kl := &e.kl
	for j := range 2 {
		if kl.stub(w, cur, j) == stubNone {
			kl.setStub(w, cur, j, stub)
			return true
		}
	}
	return false // two stubs already chosen at this cell: total degree would exceed 2
}

// joinFragments adds the internal edge between the current cell (slot cur) and
// a processed neighbour (slot a). A same-label merge is a premature cycle and is
// rejected; joining two fragments that each carry a closed terminal completes a
// path fragment — its closing pair is emitted and the label disappears.
func (e *engine) joinFragments(w []uint64, cur, a int) bool {
	kl := &e.kl
	if kl.totalDeg(w, a) >= 2 || kl.deg(w, cur) >= 2 {
		return false
	}
	la, lc := kl.label(w, a), kl.label(w, cur)
	if la == lc {
		return false // closing a cycle through the new edge
	}
	ta, tc := kl.tag(w, la), kl.tag(w, lc)
	kl.setDeg(w, a, kl.deg(w, a)+1)
	kl.setDeg(w, cur, kl.deg(w, cur)+1)
	if ta != tagNone && tc != tagNone {
		kl.addPair(w, termFromTag(ta), termFromTag(tc))
		kl.clearLabel(w, la)
		kl.clearLabel(w, lc)
		return true
	}
	kl.replaceLabel(w, lc, la)
	return true
}

// applyDrop applies the exit rule of a cell leaving the window: its total degree
// (internal edges plus stubs) must reach 2, or exactly 1 for a Paths global end.
func (e *engine) applyDrop(w []uint64, u int) bool {
	switch e.kl.deg(w, u) {
	case 2:
		e.kl.clearSlot(w, u)
		return true
	case 1:
		return e.closeEnd(w, u)
	case 0:
		return e.closeIsolated(w, u)
	}
	return false // an empty slot due for its drop is a geometry bug
}

// closeEnd finalizes a fragment end at slot u (internal degree 1). The terminal
// there is the cell's single stub, or — when the cell has no stub — a tour end
// of this half (Paths only, ε-bounded); its position never enters σ. Closing a
// terminal on the fragment either completes it (the other end was closed before:
// emit the matching pair) or waits while the fragment keeps a live cell.
func (e *engine) closeEnd(w []uint64, u int) bool {
	kl := &e.kl
	s0, s1 := kl.stub(w, u, 0), kl.stub(w, u, 1)
	var term uint64
	switch {
	case s0 != stubNone && s1 == stubNone:
		term = s0
	case s0 == stubNone && s1 == stubNone:
		if e.fam != Paths || kl.ends(w) >= 2 {
			return false
		}
		kl.setEnds(w, kl.ends(w)+1)
		term = termGlobal
	default:
		return false // two stubs at an internal end: total degree ≥ 3, unreachable
	}
	e.closeTerminalOnFragment(w, u, term)
	return true
}

// closeTerminalOnFragment records a closed terminal at the fragment of slot u:
// with a tag already set the pair completes and is emitted (the remaining cells
// of a completed fragment all sit at internal degree 2); otherwise the tag waits
// for the other end while some live cell of the fragment survives.
func (e *engine) closeTerminalOnFragment(w []uint64, u int, term uint64) {
	kl := &e.kl
	l := kl.label(w, u)
	if t := kl.tag(w, l); t != tagNone {
		kl.addPair(w, termFromTag(t), term)
		kl.clearLabel(w, l)
	} else {
		kl.setTag(w, l, tagOfTerm(term))
	}
	kl.clearSlot(w, u)
}

// closeIsolated finalizes a degree-0 cell: it is a single-vertex fragment whose
// two terminals are its stubs, or one stub plus a tour end under Paths (pair
// (stub, G)); with no stubs the cell has total degree 0 and no tour exists.
func (e *engine) closeIsolated(w []uint64, u int) bool {
	kl := &e.kl
	s0, s1 := kl.stub(w, u, 0), kl.stub(w, u, 1)
	switch {
	case s0 != stubNone && s1 != stubNone:
		kl.addPair(w, s0, s1)
	case s0 != stubNone || s1 != stubNone:
		if e.fam != Paths || kl.ends(w) >= 2 {
			return false
		}
		kl.setEnds(w, kl.ends(w)+1)
		s := s0
		if s == stubNone {
			s = s1
		}
		kl.addPair(w, s, termGlobal)
	default:
		return false // total degree 0: no tour visits a cell with no edge
	}
	kl.clearSlot(w, u)
	return true
}

// normalize checks the per-fragment invariant (live ends plus at most one closed
// terminal equals two for every label), the ε and pair-list bounds, then brings
// the key to its canonical bitwise form.
func (e *engine) normalize(w []uint64) bool {
	kl := &e.kl
	if kl.ends(w) > 2 || kl.pairCount(w) > kl.pcap {
		return false
	}
	var live [maxLabels]uint8
	for i := range kl.nSlots {
		d := kl.deg(w, i)
		if d == degEmpty {
			continue
		}
		l := kl.label(w, i)
		if l >= maxLabels {
			return false
		}
		live[l] += uint8(2 - d)
	}
	for l := range kl.nSlots + 1 {
		tagged := kl.tag(w, uint64(l)) != tagNone
		if live[l] == 0 && !tagged {
			continue
		}
		if int(live[l])+btoi(tagged) != 2 {
			return false
		}
	}
	kl.canonicalize(w)
	return true
}

// termFromTag decodes a closed-terminal tag into its terminal id.
func termFromTag(t uint64) uint64 {
	if t == tagGlobal {
		return termGlobal
	}
	return t - tagBias
}

// tagOfTerm encodes a terminal id as a closed-terminal tag.
func tagOfTerm(t uint64) uint64 {
	if t == termGlobal {
		return tagGlobal
	}
	return t + tagBias
}

// btoi converts a bool to 0/1 for the invariant sum.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
