package split

import (
	"math/bits"
	"slices"
)

// sigRec is one decoded interface state σ of a half: the seam-edge set S as a
// bitmask, the ε counter, the weight N(σ) and the emitted matching — pairs of
// terminal ids (seam index or termGlobal) stored flat in the arena at
// [off, off+2·np). The offset is uint64: the arena grows with the decoded pair
// volume and must never wrap silently.
type sigRec struct {
	smask uint64
	w     uint64
	off   uint64
	ends  uint8
	np    uint8
}

// collect decodes a finished σ table into records appended to arena (one shared
// arena spans both halves' records). A non-nil perm is applied to every seam
// terminal before decoding — this is how the mirror variant turns one built table
// into its reflected copy (N_R(σ) = N_L(ρσ)). The scan follows table slot order,
// so the record list and its weights are deterministic.
func (e *engine) collect(t *table, perm []int, arena []uint8) (recs []sigRec, out []uint8) {
	kl := &e.kl
	recs = make([]sigRec, 0, t.n)
	out = slices.Grow(arena, 2*t.n)
	stride := t.stride
	for i := 0; i < len(t.buf); i += stride {
		w := t.slotWeight(i)
		if w == 0 {
			continue
		}
		k := t.buf[i : i+kl.kw]
		pc := kl.pairCount(k)
		rec := sigRec{ends: uint8(kl.ends(k)), np: uint8(pc), off: uint64(len(out)), w: w}
		for p := range pc {
			v := kl.pairAt(k, p)
			a, b := relabel(v>>stubBits, perm), relabel(v&stubMask, perm)
			if a > b {
				a, b = b, a
			}
			out = append(out, uint8(a), uint8(b))
			rec.smask |= smaskOf(a) | smaskOf(b)
		}
		recs = append(recs, rec)
	}
	return recs, out
}

// relabel maps a seam terminal through the mirror permutation; global ends are
// position-free and pass through.
func relabel(t uint64, perm []int) uint64 {
	if perm == nil || t == termGlobal {
		return t
	}
	return uint64(perm[t])
}

// smaskOf returns the bitmask of a seam terminal (zero for a global end).
func smaskOf(t uint64) uint64 {
	if t == termGlobal {
		return 0
	}
	return 1 << t
}

// glue sums Σ N_L(σ)·N_R(σ̄) over compatible states: halves share one S, Paths
// additionally need ε_L + ε_R = 2, and the auxiliary graph G_σ (vertices: the
// seam edges of S plus two global-end markers; edges: the matching pairs M_L and
// M_R) must be connected — that is exactly "the union is a single path/cycle".
// joins counts every same-S pair examined. The sum is commutative, so neither it
// nor the join count depends on iteration order (determinism invariant).
func (e *engine) glue(recL, recR []sigRec, arena []uint8) (total, joins uint64) {
	lgroups := groupByS(recL)
	rgroups := groupByS(recR)
	masks := make([]uint64, 0, len(lgroups))
	for s := range lgroups {
		if _, ok := rgroups[s]; ok {
			masks = append(masks, s)
		}
	}
	slices.Sort(masks)
	for _, s := range masks {
		lg, rg := lgroups[s], rgroups[s]
		joins += uint64(len(lg) * len(rg))
		cb := newConnBase(s)
		for _, rl := range lg {
			for _, rr := range rg {
				if e.fam == Paths && int(rl.ends)+int(rr.ends) != 2 {
					continue
				}
				if connected(cb, rl, rr, arena) {
					total += rl.w * rr.w
				}
			}
		}
	}
	return total, joins
}

// groupByS buckets records by their seam-set mask.
func groupByS(recs []sigRec) map[uint64][]sigRec {
	m := make(map[uint64][]sigRec, len(recs))
	for _, r := range recs {
		m[r.smask] = append(m[r.smask], r)
	}
	return m
}

// maxSeamVerts bounds |S| + 2 over the contract range (seam ≤ 40 at n = 8).
const maxSeamVerts = 64

// connBase numbers the stubs of one S group: G_σ vertices are 0..|S|−1 for the
// seam edges, then the two global-end markers.
type connBase struct {
	pos [maxSeamVerts]int8 // stub index → vertex id, −1 when not in S
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

// connState is the union-find of one G_σ check; markers are allocated to the
// global-end occurrences of both halves in (L, R) order — any bijection onto the
// two real tour ends is isomorphic for connectivity, so the choice is free.
type connState struct {
	cb   connBase
	uf   [maxSeamVerts + 2]uint8
	next int
}

// vertex maps a terminal id to its G_σ vertex, minting marker vertices in order.
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

// edges unions the matching pairs of one half's record into G_σ.
func (cs *connState) edges(r sigRec, arena []uint8) {
	for p := range int(r.np) {
		a, b := arena[r.off+2*uint64(p)], arena[r.off+2*uint64(p)+1]
		cs.link(cs.vertex(a), cs.vertex(b))
	}
}

// connected builds G_σ from the two records and reports whether it is a single
// component (and therefore the union of halves is one Hamiltonian path/cycle).
func connected(cb connBase, rl, rr sigRec, arena []uint8) bool {
	cs := &connState{cb: cb, next: cb.n}
	for i := range cs.uf {
		cs.uf[i] = uint8(i)
	}
	cs.edges(rl, arena)
	cs.edges(rr, arena)
	if cs.next-cb.n != int(rl.ends)+int(rr.ends) || cs.next < 2 {
		return false // marker occurrences must match the recorded ε counters
	}
	root := cs.find(0)
	for v := 1; v < cs.next; v++ {
		if cs.find(v) != root {
			return false
		}
	}
	return true
}
