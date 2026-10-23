package split

// Field encodings inside a packed DP key. A window slot stores its internal
// degree (degEmpty marks a slot no live cell occupies), the connectivity label
// of its partial fragment and up to two chosen seam-edge indices; per-label
// closed terminals, the Paths end counter, the pair count and the sorted list
// of already emitted interface pairs share the remaining bits. Terminal ids are
// seam indices 0..62; termGlobal (63) denotes a local global end, whose board
// position never enters σ — only the ε counter does.
const (
	degBits   = 2
	degMask   = (1 << degBits) - 1
	degEmpty  = 3 // explicit empty slot: not processed yet or already dropped
	labBits   = 5
	labMask   = (1 << labBits) - 1
	stubBits  = 6
	stubMask  = (1 << stubBits) - 1
	tagBits   = 6
	tagMask   = (1 << tagBits) - 1
	pairBits  = 12
	pairMask  = (1 << pairBits) - 1
	endsBits  = 2
	pcBits    = 5
	stubPair  = 2 * stubBits // one slot's two stub fields packed side by side
	maxLabels = 3*maxHalfCols + 1

	tagNone   = 0
	tagGlobal = 1
	tagBias   = 2 // tag value of seam index e is e + tagBias (fits tagBits for n ≤ 8)

	termGlobal = stubMask // 63: global-end terminal id in pair words
	stubNone   = stubMask // 63: no stub chosen in this slot field
)

// maxHalfCols bounds ⌈n/2⌉ over the contract range n ≤ 8; maxKeyWords bounds a
// packed key for the largest supported board (9 words at n = 8, see newKeyLayout).
const (
	maxHalfCols = 4
	maxKeyWords = 10
)

// keyLayout fixes the bit offsets of every field family for one board. Fields
// are laid out contiguously: degrees, ends counter, pair count, labels, slot
// stubs, per-label closed tags, pair words. Unused high bits stay zero, so two
// equal interface configurations are bitwise equal without an occupancy word.
type keyLayout struct {
	nSlots int // window slots of the wider half (both halves share one layout)
	pcap   int // pair capacity: seam/2 + 1 (one (G,G) pair beyond stub pairs)
	kw     int // uint64 words per packed key

	offDeg, offEnds, offPC int
	offLab, offStub        int
	offTag                 int
	offPair                int
}

// newKeyLayout computes the layout for a cut geometry. n ≤ 8 is validated by
// the caller (Count), which bounds nSlots ≤ 12 and keeps kw < maxKeyWords; the
// bound is rechecked here so an out-of-range board can never overflow key buffers.
func newKeyLayout(g *cutGeom) (keyLayout, bool) {
	kl := keyLayout{
		nSlots: max(g.left.nSlots, g.right.nSlots),
		pcap:   len(g.seam)/2 + 1,
	}
	kl.offDeg = 0
	kl.offEnds = kl.offDeg + degBits*kl.nSlots
	kl.offPC = kl.offEnds + endsBits
	kl.offLab = kl.offPC + pcBits
	kl.offStub = kl.offLab + labBits*kl.nSlots
	kl.offTag = kl.offStub + stubPair*kl.nSlots
	kl.offPair = kl.offTag + tagBits*(kl.nSlots+1)
	bits := kl.offPair + pairBits*kl.pcap
	kl.kw = (bits + 63) / 64
	return kl, kl.kw <= maxKeyWords && kl.nSlots < maxLabels
}

// bits reads an unsigned field that may cross a word boundary.
func (kl *keyLayout) bits(w []uint64, off int, n uint) uint64 {
	i, s := off>>6, uint(off&63)
	v := w[i] >> s
	if s+n > 64 {
		v |= w[i+1] << (64 - s)
	}
	return v & (1<<n - 1)
}

// setBits writes an unsigned field that may cross a word boundary.
func (kl *keyLayout) setBits(w []uint64, off int, n uint, v uint64) {
	i, s := off>>6, uint(off&63)
	if s+n <= 64 {
		m := (uint64(1)<<n - 1) << s
		w[i] = w[i]&^m | (v&(uint64(1)<<n-1))<<s
		return
	}
	loMask := ^uint64(0) << s
	w[i] = w[i]&^loMask | v<<s
	hiN := n - (64 - s)
	w[i+1] = w[i+1]&^(uint64(1)<<hiN-1) | v>>(64-s)
}

// deg returns the internal-degree field of slot i.
func (kl *keyLayout) deg(w []uint64, i int) uint64 {
	return kl.bits(w, kl.offDeg+degBits*i, degBits)
}

// setDeg writes the internal-degree field of slot i.
func (kl *keyLayout) setDeg(w []uint64, i int, d uint64) {
	kl.setBits(w, kl.offDeg+degBits*i, degBits, d)
}

// label returns the fragment label of slot i.
func (kl *keyLayout) label(w []uint64, i int) uint64 {
	return kl.bits(w, kl.offLab+labBits*i, labBits)
}

// setLabel writes the fragment label of slot i.
func (kl *keyLayout) setLabel(w []uint64, i int, l uint64) {
	kl.setBits(w, kl.offLab+labBits*i, labBits, l)
}

// stub returns the j-th seam-index field of slot i (j ∈ {0,1}).
func (kl *keyLayout) stub(w []uint64, i, j int) uint64 {
	return kl.bits(w, kl.offStub+stubPair*i+stubBits*j, stubBits)
}

// setStub writes the j-th seam-index field of slot i.
func (kl *keyLayout) setStub(w []uint64, i, j int, s uint64) {
	kl.setBits(w, kl.offStub+stubPair*i+stubBits*j, stubBits, s)
}

// tag returns the closed-terminal tag of label l (tagNone when unset).
func (kl *keyLayout) tag(w []uint64, l uint64) uint64 {
	return kl.bits(w, kl.offTag+tagBits*int(l), tagBits)
}

// setTag writes the closed-terminal tag of label l.
func (kl *keyLayout) setTag(w []uint64, l, t uint64) {
	kl.setBits(w, kl.offTag+tagBits*int(l), tagBits, t)
}

// ends returns the Paths counter of finalized global ends in this half.
func (kl *keyLayout) ends(w []uint64) uint64 { return kl.bits(w, kl.offEnds, endsBits) }

// setEnds writes the global-end counter.
func (kl *keyLayout) setEnds(w []uint64, e uint64) { kl.setBits(w, kl.offEnds, endsBits, e) }

// pairCount returns the number of emitted interface pairs.
func (kl *keyLayout) pairCount(w []uint64) int {
	return int(kl.bits(w, kl.offPC, pcBits))
}

// pairAt returns the i-th emitted pair as its packed (a<<6|b) value, a ≤ b.
func (kl *keyLayout) pairAt(w []uint64, i int) uint64 {
	return kl.bits(w, kl.offPair+pairBits*i, pairBits)
}

// addPair inserts terminal pair (a, b) into the sorted pair list; every terminal
// is emitted once by construction, so no duplicate check is needed.
func (kl *keyLayout) addPair(w []uint64, a, b uint64) {
	if a > b {
		a, b = b, a
	}
	v := a<<stubBits | b
	n := kl.pairCount(w)
	for i := range n {
		if v < kl.pairAt(w, i) {
			kl.shiftPairsUp(w, i, n)
			kl.setBits(w, kl.offPair+pairBits*i, pairBits, v)
			kl.setBits(w, kl.offPC, pcBits, uint64(n+1))
			return
		}
	}
	kl.setBits(w, kl.offPair+pairBits*n, pairBits, v)
	kl.setBits(w, kl.offPC, pcBits, uint64(n+1))
}

// shiftPairsUp moves pairs [i, n) one position up to open room for an insert.
func (kl *keyLayout) shiftPairsUp(w []uint64, i, n int) {
	for j := n - 1; j >= i; j-- {
		kl.setBits(w, kl.offPair+pairBits*(j+1), pairBits, kl.pairAt(w, j))
	}
}

// stubCount returns how many seam stubs slot i carries.
func (kl *keyLayout) stubCount(w []uint64, i int) uint64 {
	var c uint64
	for j := range 2 {
		if kl.stub(w, i, j) != stubNone {
			c++
		}
	}
	return c
}

// totalDeg returns the finalized-structure degree of slot i so far: internal
// edges plus chosen seam stubs.
func (kl *keyLayout) totalDeg(w []uint64, i int) uint64 {
	return kl.deg(w, i) + kl.stubCount(w, i)
}

// maxLabel returns the largest label over live slots; callers mint a fresh
// unique label as maxLabel+1.
func (kl *keyLayout) maxLabel(w []uint64) uint64 {
	m := uint64(0)
	for i := range kl.nSlots {
		if kl.deg(w, i) != degEmpty {
			m = max(m, kl.label(w, i))
		}
	}
	return m
}

// clearSlot marks slot i empty: no degree, no label, no stubs.
func (kl *keyLayout) clearSlot(w []uint64, i int) {
	kl.setDeg(w, i, degEmpty)
	kl.setLabel(w, i, 0)
	kl.setStub(w, i, 0, stubNone)
	kl.setStub(w, i, 1, stubNone)
}

// clearLabel removes label l from every slot and clears its tag; the caller has
// just emitted the closing pair of the completed fragment.
func (kl *keyLayout) clearLabel(w []uint64, l uint64) {
	for i := range kl.nSlots {
		if kl.deg(w, i) != degEmpty && kl.label(w, i) == l {
			kl.setLabel(w, i, 0)
		}
	}
	kl.setTag(w, l, tagNone)
}

// replaceLabel renames every live slot carrying label from to label to and moves
// the closed tag along; at most one of the two labels may carry a tag (a merge
// closing two tagged fragments emits its pair instead of merging).
func (kl *keyLayout) replaceLabel(w []uint64, from, to uint64) {
	if from == to {
		return
	}
	if t := kl.tag(w, from); t != tagNone {
		kl.setTag(w, to, t)
		kl.setTag(w, from, tagNone)
	}
	for i := range kl.nSlots {
		if kl.deg(w, i) != degEmpty && kl.label(w, i) == from {
			kl.setLabel(w, i, to)
		}
	}
}

// canonicalize renumbers fragment labels by first occurrence in slot order and
// permutes the closed-tag array along, so equal partial configurations share one
// bitwise key. Empty slots carry label 0; unused tags stay tagNone.
func (kl *keyLayout) canonicalize(w []uint64) {
	var rem [maxLabels]uint8 // new label + 1 per old label; 0 means not seen
	var movedTag [maxLabels]uint64
	next := uint8(0)
	for i := range kl.nSlots {
		if kl.deg(w, i) == degEmpty {
			kl.setLabel(w, i, 0)
			continue
		}
		l := kl.label(w, i)
		if rem[l] == 0 {
			next++
			rem[l] = next
			movedTag[next-1] = kl.tag(w, l)
		}
		kl.setLabel(w, i, uint64(rem[l]-1))
	}
	for l := range kl.nSlots + 1 {
		kl.setTag(w, uint64(l), tagNone)
	}
	for i := range next {
		kl.setTag(w, uint64(i), movedTag[i])
	}
}
