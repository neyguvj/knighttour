package zdd

import (
	"math/bits"
	"slices"
)

// Glue sums the board total Σ N_L(σ)·N_R(σ̄) over compatible interface
// states: halves share one seam set S, Paths additionally require ε_L+ε_R = 2,
// and the auxiliary graph G_σ (vertices: the stubs of S plus one marker per
// global-end occurrence; edges: the matching pairs of both halves) must be
// connected — exactly "the union is a single path/cycle" (formalism plan 26).
// It returns the number of edge sets (directed tours = 2×total) and the count
// of same-S pairs examined. An empty table on either side yields total 0,
// which is not an error. The sum is commutative, so neither it nor joins
// depends on map iteration order.
func Glue(left, right map[Sigma]uint64, fam Family) (total, joins uint64) {
	lgroups := groupByS(left)
	rgroups := groupByS(right)
	masks := make([]uint64, 0, len(lgroups))
	for s := range lgroups {
		if _, ok := rgroups[s]; ok {
			masks = append(masks, s)
		}
	}
	slices.Sort(masks)
	for _, s := range masks {
		lg, rg := lgroups[s], rgroups[s]
		joins += uint64(len(lg)) * uint64(len(rg))
		for _, ls := range lg {
			for _, rs := range rg {
				if fam == Paths && int(ls.ends)+int(rs.ends) != 2 {
					continue
				}
				if connectedG(s, ls, rs) {
					total += left[ls] * right[rs]
				}
			}
		}
	}
	return total, joins
}

// groupByS buckets a σ table by its seam mask.
func groupByS(t map[Sigma]uint64) map[uint64][]Sigma {
	g := make(map[uint64][]Sigma, len(t))
	for s := range t {
		g[s.sMask] = append(g[s.sMask], s)
	}
	return g
}

// maxGlueVerts bounds |S| + global markers over the contract range (seam ≤
// 40 stubs, ε ≤ 2 per half).
const maxGlueVerts = 64

// connBase numbers the G_σ vertices of one S group: stubs get ids 0..n−1 by
// ascending bit position, global-end markers are minted above that.
type connBase struct {
	pos [maxGlueVerts]int8 // stub index → vertex id, −1 when not in S
	n   int                // seam vertices of the group
}

// newConnBase numbers the stubs of smask by ascending bit position.
func newConnBase(smask uint64) connBase {
	var cb connBase
	for i := range cb.pos {
		cb.pos[i] = -1
	}
	for s := smask; s != 0; s &= s - 1 {
		cb.pos[bits.TrailingZeros64(s)] = int8(cb.n)
		cb.n++
	}
	return cb
}

// connState is the union-find of one G_σ check.
type connState struct {
	cb   connBase
	uf   [maxGlueVerts + 2]uint8
	next int // next free vertex id (markers mint from here)
}

// vertex maps a terminal id to its G_σ vertex, minting markers for globals.
func (cs *connState) vertex(t uint8) int {
	if t != termGlobal {
		return int(cs.cb.pos[t])
	}
	v := cs.next
	cs.next++
	return v
}

// find returns the union-find root of v with path halving.
func (cs *connState) find(v int) int {
	for int(cs.uf[v]) != v {
		cs.uf[v] = cs.uf[cs.uf[v]]
		v = int(cs.uf[v])
	}
	return v
}

// link merges the components of a and b.
func (cs *connState) link(a, b int) {
	ra, rb := cs.find(a), cs.find(b)
	if ra != rb {
		cs.uf[rb] = uint8(ra)
	}
}

// connectedG builds G_σ from the two matchings and reports whether it is a
// single component. The number of minted markers must equal the recorded ε
// counters (a hand-built inconsistent σ fails the check, not the count), and
// at least two vertices must exist: an empty group is vacuously disconnected.
func connectedG(smask uint64, l, r Sigma) bool {
	cb := newConnBase(smask)
	cs := &connState{cb: cb, next: cb.n}
	for i := range cs.uf {
		cs.uf[i] = uint8(i)
	}
	markers := 0
	for _, sg := range [2]Sigma{l, r} {
		for _, p := range sg.pairs() {
			for _, t := range p {
				if t == termGlobal {
					markers++
				}
			}
			cs.link(cs.vertex(p[0]), cs.vertex(p[1]))
		}
	}
	if markers != int(l.ends)+int(r.ends) || cs.next < 2 {
		return false
	}
	root := cs.find(0)
	for v := 1; v < cs.next; v++ {
		if cs.find(v) != root {
			return false
		}
	}
	return true
}
