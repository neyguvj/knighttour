// Package zdd builds a zero-suppressed decision diagram (ZDD) of the edge-set
// family that forms the Hamiltonian paths or cycles of a graph and counts the
// family as a weight over the DAG.
//
// The construction is frontier-based: edges are decided one by one in a fixed
// linear order, and every node carries the degrees and connectivity of the
// boundary vertices implied by the decisions taken so far (plan 23). Equal
// residual subfunctions are merged through a unique table, so the diagram
// stays reduced. Building is single-threaded and deterministic: the same graph
// and edge order yield the same diagram (node ids, weights, levels) across
// runs and processes; no runtime map participates in the build path. A soft
// byte ceiling over the summed build structures stops an oversized build with
// an OverCapError before it exhausts process memory.
package zdd

import "bytes"

const (
	idNil  uint32 = iota // "no node": empty table slot and arena sentinel index
	idZero               // terminal of the empty family
	idOne                // terminal of the family {∅}
	numTerminals
)

// maxVerts bounds the vertex count: frontier slots and labels are packed into
// single bytes (degree in 3 low bits, component label above them), which keeps
// every board of the project (8×8 = 64 cells) inside the encoding.
const maxVerts = 64

// znode is one arena node: either a diagram vertex testing the edge position
// `variable`, or one of the two terminals. weight is the number of family sets
// rooted at this node, exact uint64 without saturation (plan 23 budget: the
// totals of boards 5×5–8×8 stay far below 2^64).
type znode struct {
	variable uint32 // edge position tested here; meaningless for terminals
	low      uint32 // branch that skips the variable
	high     uint32 // branch that takes the variable
	weight   uint64 // weight(low) + weight(high)
}

// arena holds every node of one diagram in a single slice. Index 0 is the idNil
// sentinel, so table slots can use 0 for "empty"; terminals occupy indices
// idZero and idOne from the start. Nodes are only appended during a build.
type arena struct {
	nodes []znode
}

// newArena returns an arena holding just the two terminals.
func newArena() *arena {
	a := &arena{nodes: make([]znode, numTerminals)}
	a.nodes[idOne].weight = 1
	return a
}

// alloc appends the node (variable, low, high), derives its weight from the
// children, and returns its id.
func (a *arena) alloc(variable, low, high uint32) uint32 {
	a.nodes = append(a.nodes, znode{
		variable: variable,
		low:      low,
		high:     high,
		weight:   a.nodes[low].weight + a.nodes[high].weight,
	})
	return uint32(len(a.nodes) - 1)
}

// mixKey finalizes three node-key words into one hash (splitmix64 mixing).
func mixKey(variable, low, high uint64) uint64 {
	x := variable*0x9E3779B97F4A7C15 + low*0xC2B2AE3D27D4EB4F + high*0x165667B19E3779F9
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// mixBytes hashes a byte key with FNV-1a plus the same finalizer, so probe
// indices are well spread even for similar frontier states.
func mixBytes(key []byte) uint64 {
	x := uint64(14695981039346656037)
	for _, b := range key {
		x = (x ^ uint64(b)) * 1099511628211
	}
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// uniqueTable maps node keys (variable, low, high) to arena ids by open
// addressing with linear probing; slot value idNil means empty. Keys live in
// the arena itself: a probe compares fields of nodes[id], which is why every
// node stores its variable. Rehashing recomputes hashes from the arena and
// never changes set semantics, so diagram results are capacity-independent.
type uniqueTable struct {
	slots []uint32
	count int
}

// newUniqueTable returns an empty table with a modest initial capacity; it
// grows by doubling while keeping the load ≤ 0.75.
func newUniqueTable() *uniqueTable {
	return &uniqueTable{slots: make([]uint32, 1<<10)}
}

// findOrAdd returns the id of node (variable, low, high), creating it on first
// use. Zero-suppression lives here: a node whose high branch is the zero
// terminal denotes exactly the low family, so low is returned without storing
// anything — the arena never holds a node with high == idZero.
func (t *uniqueTable) findOrAdd(a *arena, variable, low, high uint32) uint32 {
	if high == idZero {
		return low
	}
	mask := uint64(len(t.slots) - 1)
	h := mixKey(uint64(variable), uint64(low), uint64(high)) & mask
	for {
		id := t.slots[h]
		if id == idNil {
			id = a.alloc(variable, low, high)
			t.slots[h] = id
			t.count++
			if t.count*4 >= len(t.slots)*3 {
				t.grow(a)
			}
			return id
		}
		n := &a.nodes[id]
		if n.variable == variable && n.low == low && n.high == high {
			return id
		}
		h = (h + 1) & mask
	}
}

// grow doubles the table and reinserts every live node, recomputing hashes
// from arena keys.
func (t *uniqueTable) grow(a *arena) {
	old := t.slots
	t.slots = make([]uint32, len(old)*2)
	mask := uint64(len(t.slots) - 1)
	for _, id := range old {
		if id == idNil {
			continue
		}
		n := &a.nodes[id]
		h := mixKey(uint64(n.variable), uint64(n.low), uint64(n.high)) & mask
		for t.slots[h] != idNil {
			h = (h + 1) & mask
		}
		t.slots[h] = id
	}
}

// memoSlot addresses one stored frontier key; length 0 marks an empty slot
// because real keys are at least three bytes long. The key offset is uint64:
// on heavy points the key arena crosses 4 GiB while still inside the default
// byte budget (the measured 6×6 Paths build keeps ~250M keys × ~15 B plus
// append spare), so a 32-bit offset would wrap and make get() slice out of
// range.
type memoSlot struct {
	keyOff uint64 // offset of the key inside memoTable.keys
	length uint32
	id     uint32 // ZDD node representing that residual subfunction
}

// memoTable maps a packed frontier key (level, per-slot degree|label, ends) to
// the ZDD node of that residual subfunction. Open addressing with linear
// probing; keys are appended to one byte arena and compared in full on a probe
// hit, so equal states always share exactly one entry. Without this memo the
// top-down construction would recompute shared subtrees exponentially often.
type memoTable struct {
	slots []memoSlot
	keys  []byte
	count int
}

// newMemoTable returns an empty table with a modest initial capacity.
func newMemoTable() *memoTable {
	return &memoTable{slots: make([]memoSlot, 1<<10)}
}

// get looks key up in the memo.
func (t *memoTable) get(key []byte) (uint32, bool) {
	mask := uint64(len(t.slots) - 1)
	h := mixBytes(key) & mask
	for {
		s := &t.slots[h]
		if s.length == 0 {
			return idNil, false
		}
		if s.length == uint32(len(key)) && bytes.Equal(t.keys[s.keyOff:s.keyOff+uint64(s.length)], key) {
			return s.id, true
		}
		h = (h + 1) & mask
	}
}

// put stores key → id; keys already present are never re-inserted by the
// build path (put runs only after a get miss).
func (t *memoTable) put(key []byte, id uint32) {
	mask := uint64(len(t.slots) - 1)
	h := mixBytes(key) & mask
	for t.slots[h].length != 0 {
		h = (h + 1) & mask
	}
	t.slots[h] = memoSlot{keyOff: uint64(len(t.keys)), length: uint32(len(key)), id: id}
	t.keys = append(t.keys, key...)
	t.count++
	if t.count*4 >= len(t.slots)*3 {
		t.grow()
	}
}

// grow doubles the table and reinserts stored keys.
func (t *memoTable) grow() {
	old := t.slots
	t.slots = make([]memoSlot, len(old)*2)
	mask := uint64(len(t.slots) - 1)
	for _, s := range old {
		if s.length == 0 {
			continue
		}
		key := t.keys[s.keyOff : s.keyOff+uint64(s.length)]
		h := mixBytes(key) & mask
		for t.slots[h].length != 0 {
			h = (h + 1) & mask
		}
		t.slots[h] = s
	}
}
