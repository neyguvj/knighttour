package halfprof

// dp.go — profile-DP engine for one half (plan 31 step 0): a layered frontier
// walk over dpWalk expanding multisets of edges per cell into a flat table of
// canonical states with uint64 weights. A state is the window content plus
// closed terminals and emitted pairs (dpkey.go); each forest of the half is
// produced exactly once (edges decided at their later endpoint, crossings at
// the lower one) and the weight of a state is the exact number of forests
// reaching it — additive across generations, no saturation checks (precedent
// split/halfprof). The finished generation holds empty windows whose pair
// lists are exactly the half profiles: they are densified into profKey and
// handed to profileSink, so the join machinery stays untouched.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
)

// dpEmitTick bounds how often the emission loop checks ctx via sink failures
// and cancellation between profile conversions.
const dpEmitTick = 1 << 16

// tickStates bounds how often a step loop checks ctx (counter of expanded
// states modulo; same spirit as tickNodes in enum.go).
const tickStates = 1 << 14

// DpLayerStat is one layer row of the census: cell-step index, live states in
// the generation after it and transitions considered during it.
type DpLayerStat struct {
	Step   int
	States int
	Trans  uint64
}

// DpCensus carries the per-half run statistics published by the step-0 leaf:
// layer rows, their peaks, total considered transitions, the largest state
// weight (uint64 width witness) and the reserved-byte peak of the tables.
type DpCensus struct {
	Side        string
	Layers      []DpLayerStat
	LayerPeak   int
	Transitions uint64
	MaxWeight   uint64
	FinalStates int
	PeakBytes   int64
}

// DpOverCapError reports a profile-DP half aborted at the soft byte ceiling,
// carrying the progress of the cutoff moment (half, layer, live states).
type DpOverCapError struct {
	Side   string
	Layer  int
	States int
	Bytes  int
	Cap    int
}

// Error describes the cutoff from the counters and the cap captured at abort time.
func (e *DpOverCapError) Error() string {
	return fmt.Sprintf("halfprof: %s profile-DP layer %d with %d live states needs %d reserved bytes, cap %d",
		e.Side, e.Layer, e.States, e.Bytes, e.Cap)
}

// Unwrap ties the error to ErrOverCap so errors.Is identifies the condition.
func (e *DpOverCapError) Unwrap() error { return ErrOverCap }

// dpEngine owns the two live generations and the census of one half run.
type dpEngine struct {
	ctx       context.Context
	err       error
	budget    *memBudget
	cur       *dpTable
	next      *dpTable
	side      string
	crossF    []int
	crossPos  []int
	cen       DpCensus
	kl        dpKeyLayout
	seamCells int
}

// runHalfDp walks one half of the board with the profile-DP engine and emits
// its profiles into sink, keeping only masks of bucket `pass` of `passes`
// (same filter semantics as the enumerator). bud accounts the two live
// generations against the soft cap (a fresh one when nil); the census reports
// per-layer state counts and transitions.
func runHalfDp(g *cutGeom, ctx context.Context, lower bool, s, t, pass, passes int, bud *memBudget, sink profileSink) (DpCensus, error) {
	dg, ok := newDpGeom(g, s, t)
	if !ok {
		return DpCensus{}, fmt.Errorf("halfprof: board %d does not fit the DP key layout", g.n)
	}
	return runHalfDpGeom(&dg, ctx, lower, pass, passes, bud, sink)
}

// runHalfDpGeom is runHalfDp over prebuilt walk geometry (shared by both
// halves of one group run).
func runHalfDpGeom(dg *dpGeom, ctx context.Context, lower bool, pass, passes int, bud *memBudget, sink profileSink) (DpCensus, error) {
	w := &dg.up
	if lower {
		w = &dg.lo
	}
	e, err := newDpEngine(dg, ctx, w.lower, bud)
	if err != nil {
		return e.cen, err
	}
	for st := range w.steps {
		if err := e.runStep(w, st); err != nil {
			return e.cen, err
		}
	}
	if err := e.emit(w.lower, sink, pass, passes); err != nil {
		return e.cen, err
	}
	if err := sink.failure(); err != nil {
		return e.cen, err
	}
	e.cen.FinalStates = e.cur.n
	return e.cen, nil
}

// newDpEngine prepares the generations and the seeded start state of one half.
func newDpEngine(dg *dpGeom, ctx context.Context, lower bool, bud *memBudget) (*dpEngine, error) {
	side := "upper"
	if lower {
		side = "lower"
	}
	if bud == nil {
		bud = &memBudget{}
	}
	e := &dpEngine{
		ctx: ctx, kl: dg.kl, budget: bud, cen: DpCensus{Side: side}, side: side,
		crossF: dg.crossF, crossPos: dg.crossPos, seamCells: dg.seamCells,
	}
	var err error
	if e.cur, err = newDpTable(e.budget, dg.kl.kw); err != nil {
		return e, e.wrapCap(err, 0)
	}
	if e.next, err = newDpTable(e.budget, dg.kl.kw); err != nil {
		return e, e.wrapCap(err, 0)
	}
	if err := e.seed(); err != nil {
		return e, e.wrapCap(err, 0)
	}
	return e, nil
}

// runStep advances one cell-step: it polls ctx, expands the generation into
// next and swaps the tables, translating every failure into the run contract.
func (e *dpEngine) runStep(w *dpWalk, st int) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	if err := e.next.reset(e.budget, e.cur.n); err != nil {
		return e.wrapCap(err, st)
	}
	trans := e.step(w, st)
	if e.err != nil {
		return e.err
	}
	if e.next.err != nil {
		return e.wrapCap(e.next.err, st)
	}
	e.cur, e.next = e.next, e.cur
	e.recordLayer(st, trans)
	return nil
}

// seed empties the working generation and inserts the single all-empty state
// of weight 1 (no cell processed, no terminal closed, no pair emitted).
func (e *dpEngine) seed() error {
	var kb [maxDpKeyWords]uint64
	k := kb[:e.kl.kw]
	for i := range e.kl.nSlots {
		e.kl.clearSlot(k, i)
	}
	e.cur.add(e.budget, k, 1)
	return e.cur.err
}

// step expands every live state of the current generation into the next one
// and returns the number of considered transitions. ctx is polled on the
// expansion counter so a long layer stays cancellable.
func (e *dpEngine) step(w *dpWalk, st int) uint64 {
	var trans uint64
	seen := 0
	stride := e.cur.stride
	for i := 0; i < len(e.cur.buf); i += stride {
		if e.next.err != nil { // refused budget: abort the tail of a dead run
			return trans
		}
		if seen&(tickStates-1) == 0 && e.ctx.Err() != nil {
			e.err = e.ctx.Err()
			return trans
		}
		seen++
		if wt := e.cur.slotWeight(i); wt != 0 {
			trans += e.expand(w, e.cur.buf[i:i+e.kl.kw], wt, st)
		}
	}
	return trans
}

// recordLayer appends the census row of a finished step and updates peaks.
func (e *dpEngine) recordLayer(st int, trans uint64) {
	e.cen.Layers = append(e.cen.Layers, DpLayerStat{Step: st, States: e.cur.n, Trans: trans})
	e.cen.LayerPeak = max(e.cen.LayerPeak, e.cur.n)
	e.cen.Transitions += trans
	e.cen.MaxWeight = max(e.cen.MaxWeight, e.cur.maxVal)
	e.cen.PeakBytes = max(e.cen.PeakBytes, e.budget.used)
}

// expand enumerates the edge subsets of size ≤ 2 from the current cell to its
// processed neighbours and crossing edges and commits each candidate; it
// returns the number of considered subsets (all candidates, legal or not).
func (e *dpEngine) expand(w *dpWalk, key []uint64, wt uint64, st int) uint64 {
	items := w.items[st]
	e.trySubset(w, key, wt, st, itemAbsent, itemAbsent)
	n := 1
	for i, a := range items {
		e.trySubset(w, key, wt, st, a, itemAbsent)
		for _, b := range items[i+1:] {
			e.trySubset(w, key, wt, st, a, b)
		}
		n += 1 + len(items[i+1:])
	}
	return uint64(n)
}

// itemAbsent marks an unused subset position; crossing items are numbered
// from -2 downward so they never collide with window slots (≥ 0).
const itemAbsent = -1

// crossItem encodes the j-th crossing edge of a step as a subset item.
func crossItem(j int) int { return -j - 2 }

// trySubset applies one edge subset to a stack copy of key, runs the drop
// rules of the step, normalises and commits the result with the state weight.
func (e *dpEngine) trySubset(w *dpWalk, key []uint64, wt uint64, st, a, b int) {
	kl := &e.kl
	var kb [maxDpKeyWords]uint64
	k := kb[:kl.kw]
	copy(k, key)
	cur := w.curSlot[st]
	e.enterCell(k, cur, w.tgt[st], w.term[st])
	if a != itemAbsent && !e.applyItem(k, w, st, cur, a) {
		return
	}
	if b != itemAbsent && !e.applyItem(k, w, st, cur, b) {
		return
	}
	for _, d := range w.drops[st] {
		if !e.dropSlot(k, d) {
			return
		}
	}
	if !e.normalize(k) {
		return
	}
	e.next.add(e.budget, k, wt)
}

// enterCell materialises the current cell in its slot: fresh label, target
// degree budget as loose, and the zero-end tag when the cell is s or t (the
// end sits at the cell from birth; drops never close it separately).
func (e *dpEngine) enterCell(k []uint64, cur int, tgt int8, term uint64) {
	kl := &e.kl
	kl.clearSlot(k, cur)
	kl.setLoose(k, cur, uint64(tgt))
	lab := kl.maxLabel(k) + 1
	kl.setLabel(k, cur, lab)
	if term != dpTermNone {
		kl.setTag(k, lab, term)
	}
}

// applyItem commits one chosen edge: an internal join at slot a ≥ 0 or the
// crossing edge selected by item a < itemAbsent.
func (e *dpEngine) applyItem(k []uint64, w *dpWalk, st, cur, item int) bool {
	if item >= 0 {
		return e.joinFragments(k, cur, item)
	}
	c := w.cross[st][-item-2]
	return e.chooseCrossing(k, cur, c)
}

// joinFragments adds the internal edge between the current cell (slot cur)
// and a processed neighbour (slot a). A same-label merge is a premature cycle
// and is rejected; joining two fragments that each carry a closed terminal
// completes a path fragment — its closing pair is emitted and the label
// disappears (all remaining cells of a completed fragment sit at loose 0).
func (e *dpEngine) joinFragments(k []uint64, cur, a int) bool {
	kl := &e.kl
	if kl.loose(k, cur) == 0 || kl.loose(k, a) == 0 {
		return false
	}
	la, lc := kl.label(k, a), kl.label(k, cur)
	if la == lc {
		return false // closing a cycle through the new edge
	}
	ta, tc := kl.tag(k, la), kl.tag(k, lc)
	kl.setLoose(k, a, kl.loose(k, a)-1)
	kl.setLoose(k, cur, kl.loose(k, cur)-1)
	if ta != dpTermNone && tc != dpTermNone {
		kl.addPair(k, ta, tc)
		kl.clearLabel(k, la)
		kl.clearLabel(k, lc)
		return true
	}
	kl.replaceLabel(k, lc, la)
	return true
}

// chooseCrossing commits one crossing edge of the current lower cell: it
// consumes a degree unit and closes its terminal on the fragment right away
// (a cut point is an end of the half forest). Two crossings at one upper seam
// cell are the maximum — a third would overflow the 2-bit mask slot.
func (e *dpEngine) chooseCrossing(k []uint64, cur int, c dpCross) bool {
	kl := &e.kl
	if kl.loose(k, cur) == 0 || e.crossingsAt(k, c.f) >= 2 {
		return false
	}
	kl.setLoose(k, cur, kl.loose(k, cur)-1)
	e.closeTerminal(k, kl.label(k, cur), termFirst+c.id)
	return true
}

// crossingsAt counts closed terminals of the crossing space that feed upper
// seam slot f: every chosen crossing is already tagged or paired, so the
// window alone never undercounts.
func (e *dpEngine) crossingsAt(k []uint64, f int) int {
	kl := &e.kl
	count := 0
	for l := range kl.nSlots + 1 {
		if t := kl.tag(k, uint64(l)); t >= termFirst && e.crossF[t-termFirst] == f {
			count++
		}
	}
	for i, n := 0, kl.pairCount(k); i < n; i++ {
		v := kl.pairAt(k, i)
		for _, t := range [2]uint64{v >> dpTagBits, v & dpTagMask} {
			if t >= termFirst && e.crossF[t-termFirst] == f {
				count++
			}
		}
	}
	return count
}

// closeTerminal records a closed terminal at the fragment of label l: with a
// tag already set the pair completes and is emitted, otherwise the tag waits
// for the other end while some loose end of the fragment survives.
func (e *dpEngine) closeTerminal(k []uint64, l, term uint64) {
	kl := &e.kl
	if old := kl.tag(k, l); old != dpTermNone {
		kl.addPair(k, old, term)
		kl.clearLabel(k, l)
		return
	}
	kl.setTag(k, l, term)
}

// dropSlot applies the exit rule of a cell leaving the window: loose 0 is a
// clean exit; an upper seam cell with defect rights closes its loose budget
// as seam defect terminals (its exact deficit — no choice); anything else is
// an unfinished degree target and rejects the state.
func (e *dpEngine) dropSlot(k []uint64, d dpDrop) bool {
	kl := &e.kl
	l := kl.loose(k, d.slot)
	switch {
	case l == dpLooseEmpty:
		return false // an empty slot due for its drop is a geometry bug
	case l == 0:
		kl.clearSlot(k, d.slot)
		return true
	case d.defectF < 0:
		return false
	}
	lab := kl.label(k, d.slot)
	if l == 2 && kl.tag(k, lab) != dpTermNone {
		return false // defect 2 on a tagged fragment is arithmetically impossible
	}
	for x := range int(l) {
		e.closeTerminal(k, lab, termFirst+2*uint64(d.defectF)+uint64(x))
	}
	kl.clearSlot(k, d.slot)
	return true
}

// normalize checks the per-fragment invariant (loose ends plus at most one
// closed terminal equal two for every label) and the pair capacity, then
// brings the key to its canonical bitwise form.
func (e *dpEngine) normalize(k []uint64) bool {
	kl := &e.kl
	if kl.pairCount(k) > kl.pcap {
		return false
	}
	var live [maxDpLabels]uint8
	for i := range kl.nSlots {
		lo := kl.loose(k, i)
		if lo == dpLooseEmpty {
			continue
		}
		l := kl.label(k, i)
		if int(l) >= kl.maxLabels {
			return false
		}
		live[l] += uint8(lo)
	}
	for l := range kl.nSlots + 1 {
		tagged := kl.tag(k, uint64(l)) != dpTermNone
		if live[l] == 0 && !tagged {
			continue
		}
		if int(live[l])+btoi(tagged) != 2 {
			return false
		}
	}
	kl.canonicalize(k)
	return true
}

// btoi converts a bool to 0/1 for the invariant sum.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// liveStates counts the entries of both working generations (nil until the
// tables exist, so a creation-time cap hit reports zero progress).
func (e *dpEngine) liveStates() int {
	n := 0
	if e.cur != nil {
		n += e.cur.n
	}
	if e.next != nil {
		n += e.next.n
	}
	return n
}

// wrapCap turns a refused budget growth into DpOverCapError filled in with
// the progress counters of the moment; any other error passes through.
func (e *dpEngine) wrapCap(err error, layer int) error {
	var ch *capHit
	if errors.As(err, &ch) {
		return &DpOverCapError{Side: e.side, Layer: layer, States: e.liveStates(), Bytes: int(ch.need), Cap: softCap}
	}
	return err
}

// emit converts every state of the finished generation into a seam profile
// and hands it to sink with the enumerator's mask-bucket filter. A final
// state whose window is not empty would be a geometry bug (every cell has a
// drop deadline inside the walk).
func (e *dpEngine) emit(lower bool, sink profileSink, pass, passes int) error {
	stride := e.cur.stride
	n := 0
	for i := 0; i < len(e.cur.buf); i += stride {
		wt := e.cur.slotWeight(i)
		if wt == 0 {
			continue
		}
		if n&(dpEmitTick-1) == 0 && e.ctx.Err() != nil {
			return e.ctx.Err()
		}
		n++
		pk, err := e.profileOf(e.cur.buf[i:i+e.kl.kw], lower)
		if err != nil {
			return err
		}
		if passes > 1 && maskBucket(pk.K, passes) != pass {
			continue
		}
		sink.add(pk, wt)
		if err := sink.failure(); err != nil {
			return err
		}
	}
	return nil
}

// occEnd is one occurrence terminal of a final state with its upper seam slot
// and its rank inside that slot: the defect index in the upper half, the
// geometry-order position of the lower cell among that upper cell's crossings
// in the lower half (the enumerator's numbering rule).
type occEnd struct {
	t    uint64
	f    int
	rank int
}

// profileOf densifies a finished state's terminal pair list into a seam
// profile key: K counts the defects per seam cell, occurrence nodes are
// numbered densely in frame order, and P is the involution of the emitted
// pairs — exactly the enumerator's encoding (profile.go), so both engines'
// tables join and compare byte-identically.
func (e *dpEngine) profileOf(k []uint64, lower bool) (profKey, error) {
	if err := e.checkWindowEmpty(k); err != nil {
		return profKey{}, err
	}
	xs, occs := e.finalOccs(k, lower)
	base, m := occBase(xs)
	var nodeOf [1 << dpTagBits]int8
	dpNodeTable(occs, base, &nodeOf)
	par, err := e.finalPairing(k, &nodeOf, m)
	if err != nil {
		return profKey{}, err
	}
	return profKey{K: maskOf(xs), P: encodePairing(par)}, nil
}

// checkWindowEmpty rejects a final state that still occupies window slots.
func (e *dpEngine) checkWindowEmpty(k []uint64) error {
	for i := range e.kl.nSlots {
		if e.kl.loose(k, i) != dpLooseEmpty {
			return fmt.Errorf("halfprof: final DP state keeps slot %d live", i)
		}
	}
	return nil
}

// finalOccs reads the pair list of a finished state: xs counts the defects
// per seam cell and occs collects every occurrence terminal with its slot and
// rank (the enumerator's numbering inputs, see occEnd).
func (e *dpEngine) finalOccs(k []uint64, lower bool) ([]int8, []occEnd) {
	kl := &e.kl
	pc := kl.pairCount(k)
	xs := make([]int8, e.seamCells)
	occs := make([]occEnd, 0, 2*pc)
	for i := range pc {
		v := kl.pairAt(k, i)
		for _, t := range [2]uint64{v >> dpTagBits, v & dpTagMask} {
			if t < termFirst {
				continue // zero ends take their nodes after the occurrence count
			}
			f, rank := e.occSlot(t, lower)
			xs[f]++
			occs = append(occs, occEnd{t: t, f: f, rank: rank})
		}
	}
	return xs, occs
}

// dpNodeTable fills nodeOf so each occurrence terminal maps to its dense profile
// node: occurrences are numbered per seam slot in (slot, rank) order, matching
// the enumerator's base[f]+k layout. The table stays caller-allocated (stack
// array) — it is rebuilt on every profile emit.
func dpNodeTable(occs []occEnd, base []int8, nodeOf *[1 << dpTagBits]int8) {
	slices.SortFunc(occs, func(a, b occEnd) int {
		if c := cmp.Compare(a.f, b.f); c != 0 {
			return c
		}
		return cmp.Compare(a.rank, b.rank)
	})
	for i := range nodeOf {
		nodeOf[i] = -1
	}
	prevF, within := -1, 0
	for _, o := range occs {
		if o.f == prevF {
			within++
		} else {
			prevF, within = o.f, 0
		}
		nodeOf[o.t] = int8(base[o.f]) + int8(within)
	}
}

// finalPairing builds the pairing involution from the emitted pair list.
func (e *dpEngine) finalPairing(k []uint64, nodeOf *[1 << dpTagBits]int8, m int) ([]int8, error) {
	kl := &e.kl
	par := make([]int8, m+2)
	for i := range par {
		par[i] = noNode
	}
	for i, pc := 0, kl.pairCount(k); i < pc; i++ {
		v := kl.pairAt(k, i)
		a, okA := dpNodeOf(nodeOf, m, v>>dpTagBits)
		b, okB := dpNodeOf(nodeOf, m, v&dpTagMask)
		if !okA || !okB || a == b || par[a] != noNode || par[b] != noNode {
			return nil, fmt.Errorf("halfprof: malformed final DP pair list")
		}
		par[a], par[b] = b, a
	}
	return par, nil
}

// occSlot maps an occurrence terminal id to its seam slot and rank (see occEnd).
func (e *dpEngine) occSlot(t uint64, lower bool) (f, rank int) {
	i := t - termFirst
	if !lower {
		return int(i / 2), int(i % 2)
	}
	return e.crossF[i], e.crossPos[i]
}

// dpNodeOf maps a terminal id to its profile node: the zero ends sit at m and
// m+1, occurrences use the dense table built from the pair list.
func dpNodeOf(nodeOf *[1 << dpTagBits]int8, m int, t uint64) (int8, bool) {
	switch t {
	case dpTermSId:
		return int8(m), true
	case dpTermTId:
		return int8(m + 1), true
	}
	n := nodeOf[t]
	return n, n >= 0
}
