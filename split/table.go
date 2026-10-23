package split

import "math/bits"

const (
	loadNum, loadDen = 3, 4 // linear probing keeps n ≤ cap·75%
	minSlots         = 1 << 8
)

// table is one DP generation: a flat open-addressed map from a packed key of kw
// words to a uint64 weight. A slot stores kw key words followed by the weight;
// a zero weight marks the slot empty because weights are positive counts. The
// hot path (probe and accumulate) allocates nothing; only growth rehashes into a
// larger array, whose bytes are reserved before the old array is released.
type table struct {
	err    error // sticky budget error: inserts short-circuit once raised
	buf    []uint64
	kw     int
	stride int
	mask   int
	growAt int
	n      int
	maxVal uint64
}

// newTable returns an empty table for keys of kw words with the minimal capacity.
func newTable(budget *memBudget, kw int) (*table, error) {
	t := &table{kw: kw, stride: kw + 1}
	if err := t.realloc(budget, minSlots); err != nil {
		return nil, err
	}
	return t, nil
}

// reset empties the table and grows it when hint entries would not fit at the
// load cap; the array is kept (never shrunk), so steady-state steps allocate zero.
func (t *table) reset(budget *memBudget, hint int) error {
	if want := neededSlots(hint); want > len(t.buf)/t.stride {
		if err := t.realloc(budget, want); err != nil {
			return err
		}
	} else {
		t.clearWeights()
	}
	t.n, t.maxVal, t.err = 0, 0, nil
	return nil
}

// clearWeights zeroes the weight word of every slot; stale keys are never read
// while their weight is zero.
func (t *table) clearWeights() {
	for i := t.kw; i < len(t.buf); i += t.stride {
		t.buf[i] = 0
	}
}

// add accumulates weight w under key k, inserting when absent. A sticky budget
// error short-circuits further inserts (the run aborts at the step boundary).
func (t *table) add(budget *memBudget, k []uint64, w uint64) {
	if t.err != nil {
		return
	}
	i := t.probe(k)
	if v := t.slotWeight(i); v != 0 {
		t.setSlotWeight(i, v+w)
		t.maxVal = max(t.maxVal, v+w)
		return
	}
	if t.n+1 > t.growAt {
		if err := t.grow(budget); err != nil {
			t.err = err
			return
		}
		i = t.probe(k) // the array moved; re-find the free slot
	}
	copy(t.buf[i:i+t.kw], k)
	t.setSlotWeight(i, w)
	t.n++
	t.maxVal = max(t.maxVal, w)
}

// probe returns the element index of k's slot (a multiple of stride), or of the
// first free slot when k is absent; the hash names a slot, not an element.
func (t *table) probe(k []uint64) int {
	for j := int(hashKey(k)) & t.mask; ; j = (j + 1) & t.mask {
		i := j * t.stride
		if t.slotWeight(i) == 0 || t.slotEqual(i, k) {
			return i
		}
	}
}

// slotEqual reports whether the key words of slot i match k.
func (t *table) slotEqual(i int, k []uint64) bool {
	for j := range t.kw {
		if t.buf[i+j] != k[j] {
			return false
		}
	}
	return true
}

// slotWeight returns the weight word of slot i.
func (t *table) slotWeight(i int) uint64 { return t.buf[i+t.kw] }

// setSlotWeight writes the weight word of slot i.
func (t *table) setSlotWeight(i int, w uint64) { t.buf[i+t.kw] = w }

// grow doubles the capacity and rehashes live entries over.
func (t *table) grow(budget *memBudget) error {
	old := t.buf
	live := t.n
	if err := t.realloc(budget, 2*(len(old)/t.stride)); err != nil {
		return err
	}
	for i := 0; i < len(old); i += t.stride {
		if w := old[i+t.kw]; w != 0 {
			t.insertNoGrow(old[i:i+t.kw], w)
		}
	}
	t.n = live
	return nil
}

// realloc swaps the backing array for newCap slots under the budget without
// copying; the new allocation is reserved before the old one is released so a
// rehash transient cannot cross the soft limit unnoticed.
func (t *table) realloc(budget *memBudget, newCap int) error {
	oldBytes := int64(len(t.buf)) * 8
	if err := budget.reserve(int64(newCap) * int64(t.stride) * 8); err != nil {
		return err
	}
	t.buf = make([]uint64, newCap*t.stride)
	budget.release(oldBytes)
	t.mask = newCap - 1
	t.growAt = newCap * loadNum / loadDen
	return nil
}

// insertNoGrow places k,w into a free slot found by probing; the caller has
// already guaranteed spare capacity.
func (t *table) insertNoGrow(k []uint64, w uint64) {
	i := t.probe(k)
	copy(t.buf[i:i+t.kw], k)
	t.setSlotWeight(i, w)
}

// neededSlots returns a power-of-two capacity whose load cap fits want entries.
func neededSlots(want int) int {
	return nextPow2(max(want*loadDen/loadNum+1, minSlots))
}

// nextPow2 rounds n up to a power of two (defined for n < 2^63).
func nextPow2(n int) int { return 1 << bits.Len(uint(n-1)) }

// hashKey mixes the key words with a splitmix-style finalizer; packed profiles
// share long constant bit runs, so a plain xor would cluster linear probing.
func hashKey(k []uint64) uint64 {
	h := uint64(len(k)) * 0x9E3779B97F4A7C15
	for _, x := range k {
		h ^= x + 0x9E3779B97F4A7C15 + (h << 6) + (h >> 2)
	}
	h ^= h >> 30
	h *= 0xBF58476D1CE4E5B9
	h ^= h >> 27
	return h * 0x94D049BB133111EB
}
