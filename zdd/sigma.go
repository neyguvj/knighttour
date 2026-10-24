package zdd

import (
	"fmt"
	"unsafe"
)

// Sigma is a canonical interface state (S, M, ε) of a half (formalism plan
// 26): the used-seam mask S, the emitted terminal matching M as sorted 12-bit
// pair words (a<<6|b with a < b; terminal = seam index or termGlobal), and
// the number ε of local global ends. The value is comparable and serves as a
// map key; Σ_σ N_H(σ) equals the half-family size.
type Sigma struct {
	sMask uint64    // S: used seam edges (bit = stub id)
	words [4]uint64 // M: up to 21 sorted pair words at 12 bits each
	ends  uint8     // ε: occurrences of termGlobal in M
}

// maxSigmaPairs bounds the matching size over the contract range: seam ≤ 40
// plus two global ends → ≤ 21 pairs (the plan-27 σ-key budget of five words).
const maxSigmaPairs = 21

// pairBits is the width of one encoded matching pair.
const pairBits = 12

// markTerm translates a state terminal mark to its σ terminal id: stubs keep
// their seam index, the global end takes termGlobal (the split convention).
func markTerm(m uint8) uint8 {
	if m == markGlobal {
		return termGlobal
	}
	return m - markStubBase
}

// termWord packs one ordered terminal pair into its canonical 12-bit word.
func termWord(t1, t2 uint8) uint16 {
	if t1 > t2 {
		t1, t2 = t2, t1
	}
	return uint16(t1)<<6 | uint16(t2)
}

// pairAt returns the i-th pair word (0 for padding slots).
func (sg Sigma) pairAt(i int) uint16 {
	return uint16(sg.words[i/5]>>(pairBits*(i%5))) & ((1 << pairBits) - 1)
}

// pairs decodes the matching: terminal-id pairs (a, b), sorted. A pair word
// is never zero (a < b forces b ≥ 1), so the first zero slot ends the scan.
func (sg Sigma) pairs() [][2]uint8 {
	out := make([][2]uint8, 0, maxSigmaPairs)
	for i := range maxSigmaPairs {
		v := sg.pairAt(i)
		if v == 0 {
			break
		}
		out = append(out, [2]uint8{uint8(v >> 6), uint8(v & 63)})
	}
	return out
}

// Sigmas returns the distribution N_H(σ) of the built half: a forward pass
// over the memoized level tables replaying every transition once per state,
// accumulating emitted pairs into σ keys. It is computed once and cached
// together with an abort error (specs/zdd.md «Мягкий потолок»): when the
// session ceiling is exceeded by the pass structures — intermediate level
// maps or the σ table itself — the pass stops and returns an *OverCapError
// with progress counters, closing the session instead of growing OOM-ward.
// Final weights are plain uint64 sums, so a successful result is independent
// of map iteration order; everything the pass reads (memo slots, transitions)
// is deterministic itself.
func (h *HalfDiagram) Sigmas() (map[Sigma]uint64, error) {
	if !h.sigmaOnce {
		h.sigma, h.sigmaErr = h.distScan()
		h.sigmaOnce = true
	}
	return h.sigma, h.sigmaErr
}

// distEntryBytes approximates the reserved bytes of one go-map entry with a
// small key and value: slot payload (string header + word) plus bucket
// amortization at the runtime's 6.5/8 load; the key's byte data adds on top.
const distEntryBytes = 48

// sigmaTableBytes estimates the reserved footprint of an N_H map under the
// same load-factor model (fixed-size key, one-word value).
func sigmaTableBytes(m map[Sigma]uint64) int64 {
	return int64(len(m)) * (int64(unsafe.Sizeof(Sigma{})) + 8) * 16 / 13
}

// budget checks the session ceiling against the registry plus est bytes of
// σ-pass structures. Exceeding it closes the session and reports the same
// error shape a build cutoff does.
func (h *HalfDiagram) budget(est int64) error {
	total := h.s.liveBytes() + est
	if total <= int64(arenaCap) {
		return nil
	}
	h.s.closed = true
	return &OverCapError{Nodes: h.nodes, Bytes: int(total), Cap: arenaCap}
}

// distScan sweeps levels 0..m−1 carrying, per state key, the distribution of
// pair sets emitted so far ("past"). Per-node merging happens through the
// shared memo keys: two parents reaching one child state merge their pasts
// before it is expanded, mirroring node sharing in the DAG. At the terminal
// level accepted states finalize their σ; Σ N_H equals the family size.
func (h *HalfDiagram) distScan() (map[Sigma]uint64, error) {
	out := make(map[Sigma]uint64)
	if h.root == idZero {
		return out, nil // empty family: no accepting path exists
	}
	b := h.b
	seed := packKey(newSeamState(b.f.width[0]), make([]byte, 0, 1))
	cur := map[string]map[string]uint64{string(seed): {"": 1}}
	for i := range h.m {
		next := make(map[string]map[string]uint64)
		b.memo.levels[i].forEach(func(_ uint32, key []byte) {
			dist := cur[string(key)]
			if len(dist) == 0 {
				return
			}
			h.expandDist(i, key, dist, next)
		})
		n, est := scanLevel(next)
		h.distEntries += uint64(n)
		if err := h.budget(est); err != nil {
			return nil, err
		}
		cur = next
	}
	if err := h.finalizeTerminals(cur, out); err != nil {
		return nil, err
	}
	return out, nil
}

// scanLevel counts the past entries of one level and estimates the bytes its
// maps reserve (the σ-pass cutoff check runs at level boundaries).
func scanLevel(next map[string]map[string]uint64) (entries, estBytes int64) {
	for k, m := range next {
		entries += int64(len(m))
		estBytes += distEntryBytes + int64(len(k))
		for p := range m {
			estBytes += distEntryBytes + int64(len(p))
		}
	}
	return entries, estBytes
}

// expandDist replays both decisions of one state into the next level's
// accumulator, extending every past with the transition's emissions. Child
// keys are packed before the sibling step overwrites the scratch state.
func (h *HalfDiagram) expandDist(i int, key []byte, dist map[string]uint64, next map[string]map[string]uint64) {
	b := h.b
	st := unpackKeySeam(key)
	for _, take := range [2]bool{true, false} {
		child, ok := b.stepSeam(i, &st, take)
		if !ok {
			continue
		}
		nextKey := string(packKey(child, make([]byte, 0, keySize(child))))
		target := next[nextKey]
		if target == nil {
			target = make(map[string]uint64)
			next[nextKey] = target
		}
		h.foldPast(target, dist, b.emit[:b.nEmit])
	}
}

// foldPast adds every (past, count) of dist into target with the emitted
// pairs merged in.
func (h *HalfDiagram) foldPast(target, dist map[string]uint64, emit [][2]uint8) {
	for past, cnt := range dist {
		target[appendPairs(past, emit)] += cnt
	}
}

// finalizeTerminals applies seamAccepts to the level-m states and folds
// their past distributions into σ weights. The ceiling is re-checked as the
// table fills (per accepted state and every 1<<20 insertions).
func (h *HalfDiagram) finalizeTerminals(cur map[string]map[string]uint64, out map[Sigma]uint64) error {
	added := 0
	for key, dist := range cur {
		st := unpackKeySeam([]byte(key))
		if !seamAccepts(h.b.fam, &st) {
			continue
		}
		for past, cnt := range dist {
			out[mustSigma(past)] += cnt
			added++
			if added&(1<<20-1) == 0 {
				if err := h.budget(sigmaTableBytes(out)); err != nil {
					return err
				}
			}
		}
		if err := h.budget(sigmaTableBytes(out)); err != nil {
			return err
		}
	}
	return nil
}

// appendPairs merges the emitted mark pairs (≤ per-transition bound) into a
// sorted past of terminal pair words; with no emissions the past is reused.
func appendPairs(past string, emit [][2]uint8) string {
	if len(emit) == 0 {
		return past
	}
	add := make([]uint16, 0, len(emit))
	for _, p := range emit {
		add = append(add, termWord(markTerm(p[0]), markTerm(p[1])))
	}
	for i := 1; i < len(add); i++ {
		for j := i; j > 0 && add[j] < add[j-1]; j-- {
			add[j], add[j-1] = add[j-1], add[j]
		}
	}
	n := len(past)/2 + len(add)
	buf := make([]byte, 0, 2*n)
	old := []byte(past)
	i, j := 0, 0
	for i < len(old) && j < len(add) {
		v := uint16(old[i])<<8 | uint16(old[i+1])
		if add[j] <= v {
			buf = appendBE(buf, add[j])
			j++
			continue
		}
		buf = appendBE(buf, v)
		i += 2
	}
	for ; i < len(old); i += 2 {
		buf = append(buf, old[i], old[i+1])
	}
	for ; j < len(add); j++ {
		buf = appendBE(buf, add[j])
	}
	return string(buf)
}

// appendBE appends a pair word big-endian.
func appendBE(buf []byte, v uint16) []byte {
	return append(buf, byte(v>>8), byte(v))
}

// mustSigma rebuilds the canonical σ from a past buffer; malformed pasts are
// internal build inconsistencies (a terminal emitted twice or an oversized
// matching) and abort loudly rather than mis-count.
func mustSigma(past string) Sigma {
	var sg Sigma
	var used uint64
	for i := 0; i+1 < len(past); i += 2 {
		v := uint16(past[i])<<8 | uint16(past[i+1])
		a, b := uint8(v>>6), uint8(v&63)
		if a > b || (a == b && a != termGlobal) {
			panic(fmt.Sprintf("zdd: malformed σ pair %d,%d", a, b))
		}
		for _, t := range [2]uint8{a, b} {
			if t == termGlobal {
				sg.ends++
				continue
			}
			if used&(1<<t) != 0 {
				panic(fmt.Sprintf("zdd: seam terminal %d emitted twice", t))
			}
			used |= 1 << t
			sg.sMask |= 1 << t
		}
		idx := i / 2
		if idx >= maxSigmaPairs {
			panic(fmt.Sprintf("zdd: σ carries more than %d pairs", maxSigmaPairs))
		}
		sg.words[idx/5] |= uint64(v) << (pairBits * (idx % 5))
	}
	return sg
}
