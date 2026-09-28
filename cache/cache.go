package cache

import (
	"context"
	"iter"
	"math/bits"
	"slices"
	"sync"

	"knighttour/state"
)

// numShards is the shard count of the table; it must stay 1<<7 to match the
// shift in shardIndex.
const numShards = 128

const (
	// wBits is the packed weight width: one class W fits for every board in
	// the project's range (no runtime overflow check, specs/cache.md).
	wBits = 56
	// wMask selects the low 56 bits of a stored word: the weight part of a
	// packed pair and the slab index of a block reference.
	wMask = uint64(1)<<wBits - 1
	// tagBit set in a stored value word marks a slab reference; clear marks an
	// inline single-class pair (specs/cache.md "Формат значения").
	tagBit = uint64(1) << 63
	// classMask selects the k part (7 bits, structurally ≤ 64) of a slab ref.
	classMask = 0x7F
)

const (
	// maxClasses bounds the end-class count structurally: the classes of a
	// mask partition its cells, at most one board's worth (specs/cache.md).
	maxClasses = 64
	// numFreeClasses indexes the free baskets by log2 of the block capacity;
	// capacities run from minBlockCap (log2 1) to maxClasses (log2 6), so the
	// array spans 7 and index 0 stays unused.
	numFreeClasses = 7
	// minBlockCap is the smallest slab block: k=1 lives inline in the word, so
	// blocks start at two classes.
	minBlockCap = 2
)

// Entry is one expanded table record: mask + class tag + accumulated weight —
// the worklist element phase B and the dispatch element of the count phase
// draw from the table (one per end class, specs/cache.md). Value type of 24 B
// (mask 8 + rep 1 padded to 8 + weight 8); the batch formulas rely on this
// size.
type Entry struct {
	Mask   state.State
	Rep    uint8
	Weight uint64
}

// pack folds a class tag and weight into one stored word (rep:8 | W:56). The
// writer contract rep < 128 keeps bit 63 clear, so a packed pair never
// collides with the slab-reference tag; the same form is used for the inline
// value word and for every position of a block.
func pack(rep uint8, weight uint64) uint64 { return uint64(rep)<<wBits | weight }

// unpack splits a stored pair word back into class tag and weight.
func unpack(p uint64) (rep uint8, weight uint64) { return uint8(p >> wBits), p & wMask }

// makeRef packs a slab reference: tagBit | (k:7 | idx:56) — the class count of
// a block and its start index in the shard slab (specs/cache.md).
func makeRef(k, idx int) uint64 { return tagBit | uint64(k)<<wBits | uint64(idx)&wMask }

// slabParts splits a slab reference into its class count and block start index.
func slabParts(word uint64) (k, idx int) {
	return int((word >> wBits) & classMask), int(word & wMask)
}

// capacityFor is cap(k) = 2^⌈log₂ k⌉ — the block capacity holding k classes
// with a power-of-two growth reserve, so relocations happen only at capacity
// boundaries (specs/cache.md). Domain: k ≥ 1.
func capacityFor(k int) int { return 1 << bits.Len64(uint64(k-1)) }

// freeClass indexes the free baskets by log2 of a block capacity (a power of
// two in [minBlockCap, maxClasses]).
func freeClass(capacity int) int { return bits.TrailingZeros64(uint64(capacity)) }

// Sink is the write side of the table: an additive contributor of weighted
// end classes of a mask. Both *Cache (direct write) and *Staging (batched
// write, ADR-031) implement it, so generation descents do not care which one
// they emit into. The writer canonicalizes K and rep with the shared symmetry
// function; to this package the tag is opaque, only its range (< 128, the
// inline tag headroom) is part of the contract.
type Sink interface {
	// Set adds one contribution data[K][rep] += weight; a zero weight is a no-op.
	Set(K state.State, rep uint8, weight uint64)
}

var (
	_ Sink = (*Cache)(nil)
	_ Sink = (*Staging)(nil)
)

// shardIndex hashes the mask only, so all classes of one mask live in one
// shard — rep never takes part in the hash (specs/cache.md, ADR-004).
// Allocation-free: golden-ratio multiply, take the high bits. numShards must
// stay 1<<7 to match the shift.
func shardIndex(k state.State) int {
	h := uint64(k) * 0x9E3779B97F4A7C15
	return int(h >> (64 - 7)) // numShards = 128
}

// cacheShard is one Cache shard: the pointer-free map mask → tagged value word
// plus the writers' state of the histogram layout — the append-only slab and
// the free-block baskets — all under the writers-only Mutex. Set and
// Staging.Flush are the only lock takers; sealed reads take none (plan 16).
type cacheShard struct {
	data map[state.State]uint64 // mask → inline pair (tagBit clear) or slab ref
	slab []uint64               // append-only block storage, never shrunk or bulk-copied
	free [numFreeClasses][]int  // free block start indices per capacity class (log2 cap)
	mu   sync.Mutex
}

// Cache is the counting pipeline's single sharded "canonical mask → end-class
// histogram" table (specs/cache.md, ADR-011/ADR-018, plan 18): one type plays
// both pipeline roles — the short-lived gen-A intermediate and the task-cache
// that stays alive through the count phase. It follows the write → seal → read
// protocol: writers contribute via Set/Staging until the calling contour stops
// them (a barrier giving happens-before) and calls Seal; from then on every
// read goes through the lock-free *View handle and no writer exists. Zeros are
// never stored: neither an entry nor a class slot is created for a zero
// weight. There is no per-shard draining and no copying snapshot.
type Cache struct {
	shards [numShards]cacheShard
}

// NewCache returns an empty table (128 shards of map[state.State]uint64 plus
// the per-shard slab and free baskets under Mutex, hashed by mask via
// shardIndex).
func NewCache() *Cache {
	c := &Cache{}
	for i := range c.shards {
		c.shards[i].data = make(map[state.State]uint64)
	}
	return c
}

// Set accumulates one contribution into the class slot: data[K][rep] += w. A
// zero weight creates neither an entry nor a slot (specs/cache.md: no zeros in
// the table); contributions of one (K, rep) pile up in one slot. The input
// contract on rep (< 128, guaranteed by the shared symmetry canonicalization)
// keeps the inline form's tag bit free.
//
//nolint:gocritic // captLocal: K is the canonical-mask symbol of specs/cache.md.
func (c *Cache) Set(K state.State, rep uint8, weight uint64) {
	if weight == 0 {
		return
	}
	sh := &c.shards[shardIndex(K)]
	sh.mu.Lock()
	sh.add(K, rep, weight)
	sh.mu.Unlock()
}

// add folds one contribution into the mask's histogram under the caller's
// hold of sh.mu (specs/cache.md): an existing class slot grows in place; a new
// class opens an inline record, extends the block within its capacity reserve,
// or relocates it copy-on-append.
//
//nolint:gocritic // captLocal: K is the canonical-mask symbol of specs/cache.md.
func (sh *cacheShard) add(K state.State, rep uint8, weight uint64) {
	word := sh.data[K]
	switch {
	case word == 0: // no record yet: open one inline (k = 1)
		sh.data[K] = pack(rep, weight)
	case word&tagBit == 0:
		sh.addInline(K, word, rep, weight)
	default:
		sh.addSlab(K, word, rep, weight)
	}
}

// addInline grows a one-class record: the same class sums in place, a second
// class promotes the pair into a fresh two-class block (cap(2), specs/cache.md
// — the inline word dissolves into the block's first live position).
//
//nolint:gocritic // captLocal: K is the canonical-mask symbol of specs/cache.md.
func (sh *cacheShard) addInline(K state.State, word uint64, rep uint8, weight uint64) {
	if r, w := unpack(word); r == rep {
		sh.data[K] = pack(rep, w+weight)
		return
	}
	idx := sh.allocBlock(capacityFor(minBlockCap))
	sh.slab[idx], sh.slab[idx+1] = word, pack(rep, weight)
	sh.data[K] = makeRef(minBlockCap, idx)
}

// addSlab contributes to a multi-class record: an existing class updates its
// position in place (the tag stays), a new one lands in the capacity reserve
// when there is room, otherwise the block relocates copy-on-append.
//
//nolint:gocritic // captLocal: K is the canonical-mask symbol of specs/cache.md.
func (sh *cacheShard) addSlab(K state.State, word uint64, rep uint8, weight uint64) {
	k, idx := slabParts(word)
	if addInSlots(sh.slab[idx:idx+k], rep, weight) {
		return
	}
	if capacityFor(k+1) == capacityFor(k) {
		sh.slab[idx+k] = pack(rep, weight) // reserve room: extend the live prefix
		sh.data[K] = makeRef(k+1, idx)
		return
	}
	sh.relocate(K, idx, k, rep, weight)
}

// relocate appends a block of capacity cap(k+1), copies the k live pairs plus
// the new one into it and hands the old block to its capacity-class free
// basket (specs/cache.md copy-on-append).
//
//nolint:gocritic // captLocal: K is the canonical-mask symbol of specs/cache.md.
func (sh *cacheShard) relocate(K state.State, idx, k int, rep uint8, weight uint64) {
	oldCap := capacityFor(k)
	newIdx := sh.allocBlock(capacityFor(k + 1))
	copy(sh.slab[newIdx:newIdx+k], sh.slab[idx:idx+k])
	sh.slab[newIdx+k] = pack(rep, weight)
	sh.freeBlock(idx, oldCap)
	sh.data[K] = makeRef(k+1, newIdx)
}

// allocBlock reserves a block of the given capacity for the writers under
// sh.mu: first from that capacity class's free basket, else as fresh space at
// the slab end (standard slice doubling, amortized O(1) per word). The reserve
// tail of a recycled block may still hold its previous owner's garbage — only
// live positions are ever read (specs/cache.md).
func (sh *cacheShard) allocBlock(capacity int) int {
	basket := &sh.free[freeClass(capacity)]
	if n := len(*basket); n > 0 {
		idx := (*basket)[n-1]
		*basket = (*basket)[:n-1]
		return idx
	}
	start := len(sh.slab)
	sh.slab = slices.Grow(sh.slab, capacity)[:start+capacity]
	return start
}

// freeBlock returns a vacated block to its capacity-class free basket so the
// next block of that class reuses it instead of growing the slab.
func (sh *cacheShard) freeBlock(idx, capacity int) {
	basket := &sh.free[freeClass(capacity)]
	*basket = append(*basket, idx)
}

// addInSlots folds weight into the slot of rep in a packed slice, reporting
// whether the slot was found.
func addInSlots(slots []uint64, rep uint8, weight uint64) bool {
	for i, p := range slots {
		if r, w := unpack(p); r == rep {
			slots[i] = pack(rep, w+weight)
			return true
		}
	}
	return false
}

// Value is the read handle of one mask's end-class histogram (specs/cache.md):
// the tagged word from the map plus the shard slab backing multi-class
// records. Created only by View.Get; the zero Value means an absent mask.
// Copies are cheap and share the slab array — legal because the data is
// immutable after the seal barrier.
type Value struct {
	slab []uint64
	word uint64
}

// Weight returns the accumulated W of class rep: inline — one word comparison,
// O(1); a slab reference scans the block's live positions [idx, idx+k), O(k),
// allocation-free (specs/cache.md). A class that is not stored answers
// (0, false); so does the zero Value (word 0 is never stored — zeros are not).
func (val Value) Weight(rep uint8) (uint64, bool) {
	if val.word == 0 { // zero handle: absent mask, no class can match
		return 0, false
	}
	if val.word&tagBit == 0 {
		if r, w := unpack(val.word); r == rep {
			return w, true
		}
		return 0, false
	}
	k, idx := slabParts(val.word)
	return findWeight(val.slab[idx:idx+k], rep)
}

// findWeight scans one packed slot slice for the weight of rep.
func findWeight(slots []uint64, rep uint8) (uint64, bool) {
	for _, p := range slots {
		if r, w := unpack(p); r == rep {
			return w, true
		}
	}
	return 0, false
}

// Staging is one goroutine's batched writer into a Cache (ADR-031): emissions
// land in per-shard baskets (append, no hashing — the shard index is a
// multiply) and only become visible under one Lock per touched shard at
// Flush. It exists to cut the lock/wake churn of per-leaf Set on hot tables;
// it stores no data of its own beyond the pending basket. Not thread-safe:
// exactly one goroutine owns a Staging; concurrent Stagings of distinct
// goroutines are safe.
type Staging struct {
	cache   *Cache
	baskets [numShards][]Entry // staged contributions per destination shard
	pending int                // buffered entries across all baskets
	limit   int                // auto-flush threshold (≥ 1)
}

// NewStaging returns a batched writer into c that auto-flushes once limit
// emissions have accumulated (clamped to ≥ 1). The caller owns the returned
// Staging and must Flush it at the write boundary.
func (c *Cache) NewStaging(limit int) *Staging {
	return &Staging{cache: c, limit: max(limit, 1)}
}

// Set buffers one class contribution in its shard's basket; a zero weight is
// a no-op and does not consume the limit. Auto-flushes at the configured
// limit.
//
//nolint:gocritic // captLocal: K is the canonical-mask symbol of specs/cache.md.
func (s *Staging) Set(K state.State, rep uint8, weight uint64) {
	if weight == 0 {
		return
	}
	i := shardIndex(K)
	s.baskets[i] = append(s.baskets[i], Entry{Mask: K, Rep: rep, Weight: weight})
	s.pending++
	if s.pending == s.limit {
		s.Flush()
	}
}

// Flush folds every non-empty basket into its shard under that shard's Lock —
// duplicate (K, rep) pairs inside one batch collapse into the same slot sum
// (additive merge, commutative like Set, so the table at the phase barrier is
// identical to direct writes). Baskets keep capacity for reuse; pending
// resets.
func (s *Staging) Flush() {
	for i := range s.baskets {
		basket := s.baskets[i]
		if len(basket) == 0 {
			continue
		}
		sh := &s.cache.shards[i]
		sh.mu.Lock()
		for _, e := range basket {
			sh.add(e.Mask, e.Rep, e.Weight)
		}
		sh.mu.Unlock()
		s.baskets[i] = basket[:0] // reuse capacity; Entry is pointer-free, no clearing needed
	}
	s.pending = 0
}

// View is an immutable read handle of a sealed Cache (plan 16): lock-free Get,
// Len and the All pull-walk, all reading the live maps without any locking.
// Its precondition belongs to the calling pipeline, not to this type — it is
// created only by Seal, once every writer is done, and used while no writer
// exists; Go maps are safe for concurrent readers, so many Views (and many
// walks) coexist freely. Violating the precondition is a data race by contract.
type View struct {
	cache *Cache
}

// Seal is the read barrier of the write → seal → read protocol: it returns a
// lock-free read handle of c. Calling it after a barrier to all writers (e.g.
// workerpool.Run/Fanout returning, with each worker's final Staging.Flush)
// establishes happens-before with every write the View will observe. Repeated
// Seal yields independent handles over the same table; no writer may exist
// while any View lives (specs/cache.md).
func (c *Cache) Seal() *View { return &View{cache: c} }

// Get is one map lookup of the mask K without locking; ok is false for an
// absent mask (the zero Value). The reader's hot loop is one Get per mask,
// then Weight over the classes (specs/cache.md). Concurrent Views are safe.
//
//nolint:gocritic // captLocal: K is the canonical-mask symbol of specs/cache.md.
func (v *View) Get(K state.State) (Value, bool) {
	sh := &v.cache.shards[shardIndex(K)]
	word, found := sh.data[K]
	if !found {
		return Value{}, false
	}
	return Value{word: word, slab: sh.slab}, true
}

// Len is the number of stored records — distinct masks, NOT classes
// (specs/cache.md); constant after Seal because no writer exists any more —
// shards are summed without locking.
func (v *View) Len() int {
	total := 0
	for i := range v.cache.shards {
		total += len(v.cache.shards[i].data)
	}
	return total
}

// All is the lock-free pull-walk over every record of the sealed table, with
// no copying (specs/cache.md): values come straight out of the live maps under
// Seal's no-writers precondition. Each record unfolds into one Entry per end
// class — the inline pair, or the block's live positions in insertion order.
// Order — shard 0..127 (Go map order within a shard); ctx is checked at each
// shard boundary, a terminated ctx ends the walk there (a consumer gets a
// prefix, possibly cut inside a record). Breaking out of the range stops the
// walk early and is not an error; the iterator has no error source. Entries
// are copies by value.
func (v *View) All(ctx context.Context) iter.Seq[Entry] {
	return func(yield func(Entry) bool) {
		for i := range v.cache.shards {
			if ctx.Err() != nil {
				return
			}
			for k, word := range v.cache.shards[i].data {
				val := Value{word: word, slab: v.cache.shards[i].slab}
				if !yieldRecord(k, &val, yield) {
					return
				}
			}
		}
	}
}

// yieldRecord streams one record's classes in storage order — the single
// inline pair, or the block's live positions — and reports whether the walk
// may continue. The value is passed by pointer to keep the handle off the
// stack per call.
func yieldRecord(k state.State, val *Value, yield func(Entry) bool) bool {
	if val.word&tagBit == 0 {
		r, w := unpack(val.word)
		return yield(Entry{Mask: k, Rep: r, Weight: w})
	}
	n, idx := slabParts(val.word)
	for _, p := range val.slab[idx : idx+n] {
		r, w := unpack(p)
		if !yield(Entry{Mask: k, Rep: r, Weight: w}) {
			return false
		}
	}
	return true
}
