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

import (
	"bytes"
	"encoding/binary"
)

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

// Slot layout of the per-level memo tables (plan 24 lever A). Every slot is a
// fixed stride inside one flat byte array: a little-endian uint32 node id
// followed by the inline packed key. The empty marker is idNil — dfs never
// returns it — so occupancy shows from the id word alone, and no length field
// is needed: the level fixes the frontier width and thereby the key length.
const (
	memoIDBytes    = 4  // id word at the head of every memo slot
	memoStartSlots = 64 // initial slots per level table before the first doubling
)

// growLoad* bound the load factor of a level table: growth doubles capacity
// once occupancy reaches 7/8 (plan 24 lever A).
const (
	growLoadNum = 7
	growLoadDen = 8
)

// levelMemo is one decision level's open-addressing map from packed frontier
// states to ZDD node ids, with linear probing. Slots live in a single flat
// byte slice of stride memoIDBytes+keyLen bytes each; keys are compared in
// full on a probe hit, so equal states share exactly one entry. Without this
// memo the top-down construction would recompute shared subtrees
// exponentially often.
type levelMemo struct {
	slots  []byte // stride-byte groups: LE uint32 id + inline key; zero group = empty
	keyLen int    // bytes per key, fixed for the level (frontier width + mark byte)
	count  int    // occupied slots (nothing is ever deleted), drives growth
}

// stride returns the byte size of one slot of this level's table.
func (t *levelMemo) stride() int { return memoIDBytes + t.keyLen }

// forEach visits every stored entry's (id, key) pair in slot order. The key
// slice aliases the slot bytes and must not be retained past the callback.
// The open-seam σ pass replays the build level by level with it; slot order
// is deterministic, so the pass stays bit-reproducible.
func (t *levelMemo) forEach(fn func(id uint32, key []byte)) {
	if len(t.slots) == 0 {
		return
	}
	stride := t.stride()
	for i := 0; i < len(t.slots); i += stride {
		id := binary.LittleEndian.Uint32(t.slots[i:])
		if id != idNil {
			fn(id, t.slots[i+memoIDBytes:i+stride])
		}
	}
}

// get looks key up, returning false for an unallocated or missing entry.
func (t *levelMemo) get(key []byte) (uint32, bool) {
	if len(t.slots) == 0 {
		return idNil, false
	}
	stride := t.stride()
	mask := uint64(len(t.slots)/stride - 1)
	h := mixBytes(key) & mask
	for {
		s := t.slots[h*uint64(stride) : (h+1)*uint64(stride)]
		id := binary.LittleEndian.Uint32(s)
		if id == idNil {
			return idNil, false
		}
		if bytes.Equal(s[memoIDBytes:], key) {
			return id, true
		}
		h = (h + 1) & mask
	}
}

// put stores key → id in the first empty slot found; keys already present are
// never re-inserted by the build path (put runs only after a get miss).
func (t *levelMemo) put(key []byte, id uint32) {
	stride := t.stride()
	mask := uint64(len(t.slots)/stride - 1)
	h := mixBytes(key) & mask
	for {
		s := t.slots[h*uint64(stride) : (h+1)*uint64(stride)]
		if binary.LittleEndian.Uint32(s) == idNil {
			binary.LittleEndian.PutUint32(s, id)
			copy(s[memoIDBytes:], key)
			t.count++
			return
		}
		h = (h + 1) & mask
	}
}

// memoTables holds one levelMemo per decision level (plan 24 lever A): the
// level selects the table, so keys carry no level bytes and every key in a
// table has the same length. Up to m ≤ 168 level headers cost nothing worth
// naming; each table allocates its slots on first insert and doubles while
// loaded past growLoadNum/growLoadDen. Growth transients stay per level
// instead of doubling one global array.
//
// The bytes field is the running sum of allocated slot arrays — the memo part
// of the builder's byte budget, kept incremental so overCap stays O(1) across
// many tables. The census counters (plan 24) track insertions and zero-result
// traffic; they never change behavior and are deterministic like the build
// itself.
type memoTables struct {
	levels []levelMemo // index = edge position in [0, m); slots nil until first insert

	bytes       int64 // reserved bytes of all slot arrays (current allocations only)
	memoEntries int   // stored entries across levels = total insertions
	zeroEntries int   // entries whose value was idZero (dead-end states)
	zeroHits    int   // get hits that returned idZero
}

// newMemoTables returns an empty memo for a build with m decision levels.
func newMemoTables(m int) *memoTables {
	return &memoTables{levels: make([]levelMemo, m)}
}

// get looks the level's state key up in that level's table, counting hits
// that return the zero terminal.
func (mt *memoTables) get(level int, key []byte) (uint32, bool) {
	id, ok := mt.levels[level].get(key)
	if ok && id == idZero {
		mt.zeroHits++
	}
	return id, ok
}

// put stores key → id at the given level, allocating or growing the level's
// table as needed. All keys of a level pack to the same length because the
// frontier width is fixed per level; the first insert records that length.
func (mt *memoTables) put(level int, key []byte, id uint32) {
	mt.memoEntries++
	if id == idZero {
		mt.zeroEntries++
	}
	t := &mt.levels[level]
	if len(t.slots) == 0 {
		t.keyLen = len(key)
		mt.reserve(t, memoStartSlots)
	}
	t.put(key, id)
	if t.count*growLoadDen >= len(t.slots)/t.stride()*growLoadNum {
		mt.grow(t)
	}
}

// grow doubles the level's table and reinserts every entry, recomputing
// hashes from the inline keys.
func (mt *memoTables) grow(t *levelMemo) {
	stride := t.stride()
	old := t.slots
	mt.reserve(t, 2*len(old)/stride)
	mask := uint64(len(t.slots)/stride - 1)
	for i := 0; i < len(old); i += stride {
		id := binary.LittleEndian.Uint32(old[i:])
		if id == idNil {
			continue
		}
		key := old[i+memoIDBytes : i+stride]
		h := mixBytes(key) & mask
		for binary.LittleEndian.Uint32(t.slots[h*uint64(stride):]) != idNil {
			h = (h + 1) & mask
		}
		s := t.slots[h*uint64(stride) : (h+1)*uint64(stride)]
		binary.LittleEndian.PutUint32(s, id)
		copy(s[memoIDBytes:], key)
	}
}

// reserve gives t a slot array of nSlots entries and books the delta into the
// budget sum. Only current allocations count: the replaced array leaves the
// model even though it stays resident until the GC frees it, matching the
// previous capacity-based accounting.
func (mt *memoTables) reserve(t *levelMemo, nSlots int) {
	oldBytes := int64(len(t.slots))
	t.slots = make([]byte, nSlots*t.stride())
	mt.bytes += int64(len(t.slots)) - oldBytes
}
