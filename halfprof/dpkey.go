package halfprof

// dpkey.go — packed key layout of the profile-DP engine (plan 31 step 0). One
// key is one frontier state of a half walk: for every window slot its status
// and remaining degree budget ("loose" = target minus chosen edges) plus the
// fragment label, per-label closed terminals, and the sorted list of already
// emitted terminal pairs. The seam defect mask K and the profile pairing P are
// not stored: they are derived from the pair list of a finished state (every
// terminal appears in exactly one pair once the window is empty).
//
// Terminal ids share one 6-bit space: 0 = none, dpTermSId/dpTermTId the global
// ends, and real terminals start at termFirst. The upper half numbers its seam
// defects termFirst+2f+k (frame slot f, k-th defect there); the lower half
// uses termFirst+e for its chosen crossing edge e. Both spaces stay below 63
// for n ≤ 8 (the crossing count E of a horizontal cut is below 60 there).

// Field widths inside a packed DP key; dpLooseEmpty marks a slot no live cell
// occupies, loose values 0..2 are the remaining degree budget of an occupant.
const (
	dpLooseBits  = 2
	dpLooseMask  = (1 << dpLooseBits) - 1
	dpLooseEmpty = 3
	dpLabBits    = 5
	dpLabMask    = (1 << dpLabBits) - 1
	dpTagBits    = 6
	dpTagMask    = (1 << dpTagBits) - 1
	dpPairBits   = 12
	dpPairMask   = (1 << dpPairBits) - 1
	dpPCBits     = 6

	// maxDpKeyWords bounds a packed key for the largest supported board
	// (10 words at n = 8, see newDpKeyLayout); stack key buffers use it.
	maxDpKeyWords = 12

	// maxDpLabels bounds canonical + fresh labels over the contract range
	// (3n + 1 window slots at n ≤ 8).
	maxDpLabels = 3*8 + 1
)

// Terminal id space shared by tags and pair entries of one half run.
const (
	dpTermNone uint64 = 0
	dpTermSId  uint64 = 1
	dpTermTId  uint64 = 2
	termFirst  uint64 = 3
)

// dpKeyLayout fixes the bit offsets of every field family for one board size.
// Fields are laid out contiguously: loose budgets, labels, per-label closed
// tags, pair count, pair words. Unused high bits stay zero, so two equal
// frontier configurations are bitwise equal without an occupancy word.
type dpKeyLayout struct {
	nSlots int // window slots (3 rows x n cols)
	pcap   int // pair capacity: max terminals of a half / 2 + margin
	kw     int // uint64 words per packed key

	offLab, offTag, offPC, offPair int

	maxLabels int // canonical labels are < maxLabels
}

// newDpKeyLayout computes the layout for a board: nSlots = 3n window slots and
// a pair capacity covering every terminal of either half (upper defects 2 per
// seam cell plus two zero ends, lower crossings E plus two zero ends). It
// reports false when the board does not fit the packed representation.
func newDpKeyLayout(n, seamCells, crossings int) (dpKeyLayout, bool) {
	kl := dpKeyLayout{
		nSlots:    3 * n,
		maxLabels: 3*n + 1,
	}
	termUpper := 2*seamCells + 2
	termLower := crossings + 2
	kl.pcap = (max(termUpper, termLower) + 3) / 2

	kl.offLab = dpLooseBits * kl.nSlots
	kl.offTag = kl.offLab + dpLabBits*kl.nSlots
	kl.offPC = kl.offTag + dpTagBits*(kl.nSlots+1)
	kl.offPair = kl.offPC + dpPCBits
	bits := kl.offPair + dpPairBits*kl.pcap
	kl.kw = (bits + 63) / 64
	ok := kl.kw <= maxDpKeyWords && kl.maxLabels <= 1<<dpLabBits && kl.pcap+2 <= 1<<dpPCBits
	return kl, ok
}

// bits reads an unsigned field that may cross a word boundary.
func (kl *dpKeyLayout) bits(w []uint64, off int, n uint) uint64 {
	i, s := off>>6, uint(off&63)
	v := w[i] >> s
	if s+n > 64 {
		v |= w[i+1] << (64 - s)
	}
	return v & (1<<n - 1)
}

// setBits writes an unsigned field that may cross a word boundary.
func (kl *dpKeyLayout) setBits(w []uint64, off int, n uint, v uint64) {
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

// loose returns the remaining-degree field of slot i (dpLooseEmpty when free).
func (kl *dpKeyLayout) loose(w []uint64, i int) uint64 {
	return kl.bits(w, dpLooseBits*i, dpLooseBits)
}

// setLoose writes the remaining-degree field of slot i.
func (kl *dpKeyLayout) setLoose(w []uint64, i int, v uint64) {
	kl.setBits(w, dpLooseBits*i, dpLooseBits, v)
}

// label returns the fragment label of slot i.
func (kl *dpKeyLayout) label(w []uint64, i int) uint64 {
	return kl.bits(w, kl.offLab+dpLabBits*i, dpLabBits)
}

// setLabel writes the fragment label of slot i.
func (kl *dpKeyLayout) setLabel(w []uint64, i int, l uint64) {
	kl.setBits(w, kl.offLab+dpLabBits*i, dpLabBits, l)
}

// tag returns the closed-terminal id of label l (dpTermNone when unset).
func (kl *dpKeyLayout) tag(w []uint64, l uint64) uint64 {
	return kl.bits(w, kl.offTag+dpTagBits*int(l), dpTagBits)
}

// setTag writes the closed-terminal id of label l.
func (kl *dpKeyLayout) setTag(w []uint64, l, t uint64) {
	kl.setBits(w, kl.offTag+dpTagBits*int(l), dpTagBits, t)
}

// pairCount returns the number of emitted terminal pairs.
func (kl *dpKeyLayout) pairCount(w []uint64) int {
	return int(kl.bits(w, kl.offPC, dpPCBits))
}

// pairAt returns the i-th emitted pair as its packed (a<<6|b) value, a ≤ b.
func (kl *dpKeyLayout) pairAt(w []uint64, i int) uint64 {
	return kl.bits(w, kl.offPair+dpPairBits*i, dpPairBits)
}

// addPair inserts terminal pair (a, b) into the sorted pair list; every
// terminal is emitted once by construction, so no duplicate check is needed.
func (kl *dpKeyLayout) addPair(w []uint64, a, b uint64) {
	if a > b {
		a, b = b, a
	}
	v := a<<dpTagBits | b
	n := kl.pairCount(w)
	for i := range n {
		if v < kl.pairAt(w, i) {
			kl.shiftPairsUp(w, i, n)
			kl.setBits(w, kl.offPair+dpPairBits*i, dpPairBits, v)
			kl.setBits(w, kl.offPC, dpPCBits, uint64(n+1))
			return
		}
	}
	kl.setBits(w, kl.offPair+dpPairBits*n, dpPairBits, v)
	kl.setBits(w, kl.offPC, dpPCBits, uint64(n+1))
}

// shiftPairsUp moves pairs [i, n) one position up to open room for an insert.
func (kl *dpKeyLayout) shiftPairsUp(w []uint64, i, n int) {
	for j := n - 1; j >= i; j-- {
		kl.setBits(w, kl.offPair+dpPairBits*(j+1), dpPairBits, kl.pairAt(w, j))
	}
}

// maxLabel returns the largest label over occupied slots; callers mint a fresh
// unique label as maxLabel+1.
func (kl *dpKeyLayout) maxLabel(w []uint64) uint64 {
	m := uint64(0)
	for i := range kl.nSlots {
		if kl.loose(w, i) != dpLooseEmpty {
			m = max(m, kl.label(w, i))
		}
	}
	return m
}

// clearSlot marks slot i empty: no loose budget, no label.
func (kl *dpKeyLayout) clearSlot(w []uint64, i int) {
	kl.setLoose(w, i, dpLooseEmpty)
	kl.setLabel(w, i, 0)
}

// clearLabel removes label l from every slot and clears its tag; the caller
// has just emitted the closing pair of the completed fragment. Slots left with
// label 0 carry zero loose, so they can neither merge nor disturb the
// invariant sums.
func (kl *dpKeyLayout) clearLabel(w []uint64, l uint64) {
	for i := range kl.nSlots {
		if kl.loose(w, i) != dpLooseEmpty && kl.label(w, i) == l {
			kl.setLabel(w, i, 0)
		}
	}
	kl.setTag(w, l, dpTermNone)
}

// replaceLabel renames every live slot carrying label from to label to and
// moves the closed tag along; at most one of the two labels may carry a tag
// (a merge closing two tagged fragments emits its pair instead of merging).
func (kl *dpKeyLayout) replaceLabel(w []uint64, from, to uint64) {
	if from == to {
		return
	}
	if t := kl.tag(w, from); t != dpTermNone {
		kl.setTag(w, to, t)
		kl.setTag(w, from, dpTermNone)
	}
	for i := range kl.nSlots {
		if kl.loose(w, i) != dpLooseEmpty && kl.label(w, i) == from {
			kl.setLabel(w, i, to)
		}
	}
}

// canonicalize renumbers fragment labels by first occurrence in slot order and
// permutes the closed-tag array along, so equal partial configurations share
// one bitwise key. Empty and cleared slots carry label 0; unused tags stay
// dpTermNone.
func (kl *dpKeyLayout) canonicalize(w []uint64) {
	var rem [maxDpLabels]uint8 // new label + 1 per old label; 0 means not seen
	var movedTag [maxDpLabels]uint64
	next := uint8(0)
	for i := range kl.nSlots {
		if kl.loose(w, i) == dpLooseEmpty {
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
		kl.setTag(w, uint64(l), dpTermNone)
	}
	for i := range next {
		kl.setTag(w, uint64(i), movedTag[i])
	}
}
