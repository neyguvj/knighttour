// Package shapecount computes h(shape, end): the number of knight paths that
// cover exactly shape and end at end. The value depends only on the induced
// subgraph of the shape with a marked endpoint (knight adjacency is translation
// invariant and paths never leave the shape), so it is evaluated per D4 +
// translation class in normalized coordinates — see specs/shapecount.md.
//
// This is the stateless core of class mode: every class is computed exactly
// once by construction (the counter aggregates all multiplicities into M
// before this package is called). Per-shape memo buffers are pooled and
// logically empty on acquire — reuse never leaks entries across shapes. An
// optional per-worker persistent tail memo (TailMemo, plan 03 variant B)
// shares exact f(cur,todo) keys between the shapes one worker processes; it
// is sound because the value depends only on the pair (todo ∪ {cur}, cur).
package shapecount

import (
	"math"
	"sync"

	"knighttour/graph"
	"knighttour/pruner"
	"knighttour/state"
	"knighttour/types"
)

// DefaultTailSlots is the per-worker cap of the persistent tail memo (slots
// of 17 B in flat arrays, load factor ≤ ½): ~136 MB per worker at most.
const DefaultTailSlots = 1 << 23

// DefaultShapeFilter is the pre-DP feasibility mask New() starts with
// (plan 02: forced chains only — see SetShapeFilter).
const DefaultShapeFilter = pruner.L2ForcedChain

type Counter struct {
	graph       *graph.Graph
	pruner      *pruner.Pruner
	tailK       int             // max popcount(todo) persisted into a tail memo; 0 disables
	tailSlots   int             // per-worker tail capacity in slots
	shapeFilter pruner.L2Checks // pre-DP feasibility checks (plan 02); L2None = off
}

func New(g *graph.Graph) *Counter {
	return &Counter{
		graph:       g,
		pruner:      pruner.New(g),
		tailSlots:   DefaultTailSlots,
		shapeFilter: DefaultShapeFilter,
	}
}

// SetShapeFilter configures the pre-DP shape feasibility filter of
// CountShapeWithTail (plan 02): ends proven hopeless by these checks never
// enter the DP, and a shape whose every end is killed skips the memo buffer
// entirely. Default L2ForcedChain — stage-0 measurements showed forced chains
// kill 53–67% of zero shapes while articulation adds only ~1pp per-end
// Tarjan cost (specs/plans/02-shape-feasibility-filter.md). L2None restores
// the bit-identical pre-filter behavior. Call at construction time.
func (c *Counter) SetShapeFilter(mask pruner.L2Checks) { c.shapeFilter = mask }

// SetTailMemo configures the persistent level-2 memo (plan 03 variant B):
// states with popcount(todo) ≤ k are shared between the shapes one worker
// counts. k ≤ 0 disables the level entirely (the default). Call at
// construction time; slots caps one worker's table (0 keeps the default).
func (c *Counter) SetTailMemo(k, slots int) {
	c.tailK = max(k, 0)
	if slots > 0 {
		c.tailSlots = slots
	}
}

// NewTail allocates the per-worker persistent tail memo, or returns nil when
// the level is disabled. The table must be used by exactly one goroutine.
func (c *Counter) NewTail() *TailMemo {
	if c.tailK <= 0 {
		return nil
	}
	return newTailMemo(c.tailK, c.tailSlots)
}

// SetL2 reconfigures the enhanced DP pruning (see pruner.ShouldPruneState):
// which L2 checks run and the remainder threshold from which they apply.
// Call at construction time; benchmarks toggle it to measure single checks.
func (c *Counter) SetL2(mask pruner.L2Checks, minTodo int) {
	c.pruner.SetL2(mask, minTodo)
}

// memoTable is the DP memo f(cur,todo) -> h in open addressing over flat
// arrays. Slots carry a generation stamp: reset advances the generation so the
// whole table reads empty in O(1), which keeps pooled reuse allocation-free
// and distinguishes stored zeros from free slots. Load factor stays ≤ ½.
type memoTable struct {
	keys   []state.State
	curs   []uint8
	vals   []uint64
	stamps []uint32
	gen    uint32
	n      int
}

const (
	initialMemoCap = 1 << 4
	maxMemoCap     = 1 << 20 // slots; oversized buffers shrink on pool return
)

var memoPool = sync.Pool{New: func() any {
	return &memoTable{
		keys:   make([]state.State, initialMemoCap),
		curs:   make([]uint8, initialMemoCap),
		vals:   make([]uint64, initialMemoCap),
		stamps: make([]uint32, initialMemoCap),
	}
}}

func (m *memoTable) reset() {
	if m.gen == math.MaxUint32 {
		clear(m.stamps) // once per 2^32 acquires: make stamps==0 mean empty again
		m.gen = 0
	}
	m.gen++
	m.n = 0
}

// shrink releases memory after an outlier shape so pooled buffers stay bounded.
func (m *memoTable) shrink() bool {
	if len(m.keys) <= maxMemoCap {
		return false
	}
	c := maxMemoCap >> 2
	m.keys = make([]state.State, c)
	m.curs = make([]uint8, c)
	m.vals = make([]uint64, c)
	m.stamps = make([]uint32, c)
	m.gen++
	m.n = 0
	return true
}

func hashKey(todo state.State, cur int) uint64 {
	x := uint64(todo) ^ (uint64(cur) * 0x9E3779B97F4A7C15)
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 29
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 32
	return x
}

func (m *memoTable) get(todo state.State, cur int) (uint64, bool) {
	c := uint64(len(m.keys))
	i := hashKey(todo, cur) & (c - 1)
	for {
		if m.stamps[i] != m.gen {
			return 0, false
		}
		if m.keys[i] == todo && m.curs[i] == uint8(cur) {
			return m.vals[i], true
		}
		i = (i + 1) & (c - 1)
	}
}

func (m *memoTable) put(todo state.State, cur int, v uint64) {
	if m.n*2 >= len(m.keys) {
		m.grow()
	}
	c := uint64(len(m.keys))
	i := hashKey(todo, cur) & (c - 1)
	for m.stamps[i] == m.gen {
		if m.keys[i] == todo && m.curs[i] == uint8(cur) {
			m.vals[i] = v
			return
		}
		i = (i + 1) & (c - 1)
	}
	m.stamps[i] = m.gen
	m.keys[i] = todo
	m.curs[i] = uint8(cur)
	m.vals[i] = v
	m.n++
}

func (m *memoTable) grow() {
	oldKeys, oldCurs, oldVals, oldStamps, gen := m.keys, m.curs, m.vals, m.stamps, m.gen
	c := len(oldKeys) << 1
	m.keys = make([]state.State, c)
	m.curs = make([]uint8, c)
	m.vals = make([]uint64, c)
	m.stamps = make([]uint32, c)
	m.n = 0
	for i := range oldStamps {
		if oldStamps[i] != gen {
			continue
		}
		k := hashKey(oldKeys[i], int(oldCurs[i])) & uint64(c-1)
		for m.stamps[k] == gen {
			k = (k + 1) & uint64(c-1)
		}
		m.stamps[k] = gen
		m.keys[k] = oldKeys[i]
		m.curs[k] = oldCurs[i]
		m.vals[k] = oldVals[i]
		m.n++
	}
}

// TailMemo is the persistent level-2 memo f(cur,todo) -> h shared between all
// shapes one final-pass worker counts (plan 03 variant B). Keys are exact
// normalized coordinates: an entry written while counting one shape is valid
// for every later shape containing the same pair, because f depends only on
// (todo ∪ {cur}, cur) — no canonicalization needed. Open addressing without
// generation stamps (the table is never reset): a free slot has todo == 0,
// which walk never stores because todo == 0 returns before the memo. Growth
// doubles capacity up to maxCap; once full, writes stop — correctness never
// depends on freshness, only reuse potential does. Not safe for concurrent
// use: exactly one goroutine owns a table.
type TailMemo struct {
	keys   []state.State
	curs   []uint8
	vals   []uint64
	n      int
	k      int // persist states with popcount(todo) ≤ k only
	maxCap int
}

const initialTailCap = 1 << 14

// pow2AtLeast rounds n up to the next power of two (capacities must be masks).
func pow2AtLeast(n int) int {
	c := 1
	for c < n {
		c <<= 1
	}
	return c
}

func newTailMemo(k, maxCap int) *TailMemo {
	maxCap = pow2AtLeast(max(1, maxCap))
	c := min(initialTailCap, maxCap)
	return &TailMemo{
		keys:   make([]state.State, c),
		curs:   make([]uint8, c),
		vals:   make([]uint64, c),
		k:      k,
		maxCap: maxCap,
	}
}

func (t *TailMemo) get(todo state.State, cur int) (uint64, bool) {
	c := uint64(len(t.keys))
	i := hashKey(todo, cur) & (c - 1)
	for t.keys[i] != 0 {
		if t.keys[i] == todo && t.curs[i] == uint8(cur) {
			return t.vals[i], true
		}
		i = (i + 1) & (c - 1)
	}
	return 0, false
}

// put stores the value; when the table is at its cap and the key is new the
// write is dropped (no eviction, no reset — see specs/shapecount.md).
func (t *TailMemo) put(todo state.State, cur int, v uint64) {
	if t.n*2 >= len(t.keys) {
		if len(t.keys) == t.maxCap {
			return
		}
		t.grow()
	}
	c := uint64(len(t.keys))
	i := hashKey(todo, cur) & (c - 1)
	for t.keys[i] != 0 {
		if t.keys[i] == todo && t.curs[i] == uint8(cur) {
			t.vals[i] = v
			return
		}
		i = (i + 1) & (c - 1)
	}
	t.keys[i] = todo
	t.curs[i] = uint8(cur)
	t.vals[i] = v
	t.n++
}

func (t *TailMemo) grow() {
	oldKeys, oldCurs, oldVals := t.keys, t.curs, t.vals
	c := min(len(oldKeys)<<1, t.maxCap)
	t.keys = make([]state.State, c)
	t.curs = make([]uint8, c)
	t.vals = make([]uint64, c)
	t.n = 0
	for i := range oldKeys {
		if oldKeys[i] == 0 {
			continue
		}
		k := hashKey(oldKeys[i], int(oldCurs[i])) & uint64(c-1)
		for t.keys[k] != 0 {
			k = (k + 1) & uint64(c-1)
		}
		t.keys[k] = oldKeys[i]
		t.curs[k] = oldCurs[i]
		t.vals[k] = oldVals[i]
		t.n++
	}
}

// Entries reports how many records the tail currently holds (metrics/tests).
func (t *TailMemo) Entries() int { return t.n }

// CountShape returns h(shape, end) for every requested end (0 when the end is
// outside shape). All ends of one shape share a single memo table: subproblems
// f(cur, todo) are identical across ends of the same shape, which amortizes the
// dominant cost several-fold compared to per-end recomputation. The table comes
// from a pool and is logically empty on acquire (specs/shapecount.md).
//
// res optionally collects pruning statistics of the DP (nil is allowed): every
// pruned branch counts via CountPrune, and Finalize runs before returning, so
// the caller can fold the result straight into monitoring.ReportSubtask.
func (c *Counter) CountShape(shape state.State, ends []int, res *types.Result) []uint64 {
	return c.CountShapeWithTail(shape, ends, res, nil)
}

// CountShapeWithTail is CountShape with the optional persistent tail memo
// (plan 03 variant B): a non-nil tail lets small subproblems (popcount(todo)
// ≤ configured k) computed for one shape answer the same key in later shapes
// of the same worker. A nil tail makes this bit-identical to CountShape.
//
// inline to avoid extra calls in the memoized recursion.
//
//nolint:cyclop // hot path: the DP core, evaluated per shape class; branching is kept
func (c *Counter) CountShapeWithTail(shape state.State, ends []int, res *types.Result, tail *TailMemo) []uint64 {
	out := make([]uint64, len(ends))

	// Shape feasibility filter (plan 02): ends proven hopeless by necessary
	// conditions never enter the DP; if every end dies, the shape returns a
	// zero slice without touching the memo pool at all. The killed-set is a
	// bitmap over end positions, so no allocation on the hot path (the
	// counter feeds at most one entry per cell, hence len(ends) ≤ 64; larger
	// inputs from custom callers simply skip the filter).
	var killed state.State
	nKilled, nOutside := 0, 0
	if c.shapeFilter != pruner.L2None && len(ends) <= 64 {
		for i, e := range ends {
			if !shape.IsVisited(e) {
				nOutside++
				continue
			}
			if cut, _ := c.pruner.ShapeFeasible(e, shape, c.shapeFilter); cut {
				killed = killed.Visit(i)
				nKilled++
			}
		}
		if nKilled+nOutside == len(ends) {
			if res != nil {
				if nKilled > 0 {
					res.FilteredShapes++
				}
				res.Finalize()
			}
			return out
		}
	}

	m := memoPool.Get().(*memoTable)
	m.reset() // pooled buffers may hold previous generation's slots
	defer func() {
		if !m.shrink() {
			memoPool.Put(m)
		}
	}()

	var walk func(cur int, todo state.State) uint64
	walk = func(cur int, todo state.State) uint64 {
		if todo == 0 {
			return 1
		}
		if v, ok := m.get(todo, cur); ok {
			return v
		}
		small := tail != nil && todo.CountBits() <= tail.k
		if small {
			if res != nil {
				res.TailLookups++
			}
			if v, ok := tail.get(todo, cur); ok {
				if res != nil {
					res.TailHits++
				}
				return v
			}
		}
		if res != nil {
			res.DPStates++ // evaluated state: the "DP nodes" metric (plan 04)
		}
		// Pruning is sound here: ShouldPruneState only cuts states with no
		// completion, i.e. a true zero (L2 = articulation points and forced
		// chains over G[todo ∪ {cur}], specs/pruner.md). It runs on normalized
		// shape coordinates; knight adjacency is translation invariant and
		// board-edge clipping only drops off-board cells (never in todo), so
		// G[todo] is seen exactly.
		if pruned, reason := c.pruner.ShouldPruneState(cur, todo); pruned {
			if res != nil {
				res.CountPrune(reason)
			}
			m.put(todo, cur, 0)
			if small {
				tail.put(todo, cur, 0)
			}
			return 0
		}
		var total uint64
		for n := range c.graph.GetNeighborMask(cur).Intersect(todo).AllVisited() {
			total += walk(n, todo.Unvisit(n))
		}
		m.put(todo, cur, total)
		if small {
			tail.put(todo, cur, total)
		}
		return total
	}

	for i, e := range ends {
		if !shape.IsVisited(e) || killed.IsVisited(i) {
			continue
		}
		out[i] = walk(e, shape.Unvisit(e))
	}
	if res != nil {
		res.Finalize()
	}
	return out
}
