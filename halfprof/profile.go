package halfprof

// profile.go — seam profile keys and the alternating-links cycle check.
//
// A profile is (K, P): K packs the defect counts of the two upper seam rows
// into 2 bits per cell (0 empty, 1 end, 2 occupied by a crossing-pair), and P
// is the canonical byte encoding of an involution over the occurrence nodes
// derived from K — (seam cell, slot) pairs in frame order — plus the two zero
// ends ⊥s, ⊥t standing for the path's global endpoints. The upper table keys
// forests by their own pairing; the lower table keys a forest plus its chosen
// crossing edges by the pairing induced through the lower half. A glued pair
// is correct when the alternating walk over (P, Q) from ⊥s visits every
// occurrence exactly once and ends at ⊥t — the cc = 0 test of the source
// listing, stated as single-path coverage.

import "fmt"

// noNode marks an unmatched slot in a pairing.
const noNode int8 = -1

// profKey is one aggregation key: the seam defect mask and the canonical
// pairing bytes. Comparable so it serves as a Go map key directly.
type profKey struct {
	P string
	K uint64
}

// maskOf packs per-seam-cell defect counts (0..2) into K, two bits per frame
// slot; x must have the seam length.
func maskOf(x []int8) uint64 {
	var k uint64
	for i, v := range x {
		k |= uint64(v) << (2 * i)
	}
	return k
}

// encodePairing canonicalises an involution par over nodes [0, m+2) (par[i] == j
// implies par[j] == i; noNode for unmatched) into bytes: byte i holds par[i]+1,
// 0 for unmatched. Involutivity makes the byte string order-free canonical.
func encodePairing(par []int8) string {
	for i, p := range par {
		if p != noNode && (int(p) >= len(par) || par[p] != int8(i)) {
			panic(fmt.Sprintf("halfprof: pairing not involutive at %d", i))
		}
	}
	b := make([]byte, len(par))
	for i, p := range par {
		if p != noNode {
			b[i] = byte(p) + 1
		}
	}
	return string(b)
}

// decodePairing inverts encodePairing.
func decodePairing(s string) []int8 {
	par := make([]int8, len(s))
	for i, c := range s {
		if c == 0 {
			par[i] = noNode
		} else {
			par[i] = int8(c - 1)
		}
	}
	return par
}

// singlePathOK runs the alternating-links check over the two involutions of a
// glued pair sharing one mask: m occurrence nodes plus ⊥s at index m and ⊥t at
// m+1. Walking from ⊥s, every step leaves a node through the involution it did
// not arrive by; the join is correct (no internal cycle) exactly when the walk
// covers all occurrences once and breaks off at ⊥t.
func singlePathOK(p, q []int8, m int) bool {
	const none = -1
	visited := uint64(1) << m // ⊥s start
	prev, cur := none, m
	for {
		next := none
		if a := p[cur]; int(a) != prev && a != noNode {
			next = int(a)
		}
		if b := q[cur]; int(b) != prev && b != noNode {
			if next != none {
				return false // node of degree > 2: malformed pairing
			}
			next = int(b)
		}
		if next == none {
			break
		}
		if visited&uint64(1<<next) != 0 {
			return false // revisit: an internal cycle
		}
		visited |= uint64(1 << next)
		prev, cur = cur, next
	}
	if cur != m+1 {
		return false // broke off before ⊥t
	}
	return visited&((uint64(1)<<m)-1) == (uint64(1)<<m)-1 // all occurrences covered
}
