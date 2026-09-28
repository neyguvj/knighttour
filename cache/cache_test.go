package cache

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/state"
)

// Entry stays 24 B: the dispatch batch formulas (entryBatch 392 B, K/B/C) are
// derived from this size (specs/counter.md, plan 18).
func TestEntrySize(t *testing.T) {
	assert.Equal(t, 24, int(unsafe.Sizeof(Entry{})))
}

// slot identifies one end class of a mask — the unit of Set/Weight/All.
type slot struct {
	mask state.State
	rep  uint8
}

// walkAll collects one full All walk into a per-class map, asserting on the
// way that every delivered class matches Get+Weight for its (mask, rep), is
// never emitted twice and carries a positive weight (specs/cache.md).
func walkAll(t *testing.T, v *View) map[slot]uint64 {
	t.Helper()
	m := make(map[slot]uint64, v.Len())
	for e := range v.All(context.Background()) {
		assert.Positive(t, e.Weight, "zeros are never stored")
		s := slot{e.Mask, e.Rep}
		_, dup := m[s]
		assert.False(t, dup, "class emitted twice: %v", s)
		stored, found := v.Get(e.Mask)
		require.Truef(t, found, "Get lost mask %v", e.Mask)
		w, ok := stored.Weight(e.Rep)
		assert.Truef(t, ok, "Weight lost class %d of %v", e.Rep, e.Mask)
		assert.Equal(t, e.Weight, w)
		m[s] = e.Weight
	}
	return m
}

// TestCacheSetGetAndZeroWeight pins Set's additive per-class semantics and the
// sealed View's hit/miss/Len over them (specs/cache.md): reads exist only
// through a handle taken after the writers are done; zero weights create
// neither an entry nor a class slot.
func TestCacheSetGetAndZeroWeight(t *testing.T) {
	const m1 state.State = 0b101
	const m2 state.State = 0b1001

	tests := []struct {
		setup      func(c *Cache)
		wantWeight map[slot]uint64
		name       string
		wantAbsent []slot
		wantItems  int
	}{
		{
			name:       "miss on empty table",
			wantAbsent: []slot{{m1, 1}},
		},
		{
			name: "zero weight stores nothing — no entry, no slot",
			setup: func(c *Cache) {
				c.Set(m1, 1, 0)
			},
			wantAbsent: []slot{{m1, 1}},
		},
		{
			name: "Set sums one class slot",
			setup: func(c *Cache) {
				c.Set(m1, 1, 3)
				c.Set(m1, 1, 4)
			},
			wantItems:  1,
			wantWeight: map[slot]uint64{{m1, 1}: 7},
		},
		{
			name: "classes of one mask are distinct slots of one record",
			setup: func(c *Cache) {
				c.Set(m1, 1, 5)
				c.Set(m1, 2, 6)
			},
			wantItems:  1, // Len counts masks, not classes
			wantWeight: map[slot]uint64{{m1, 1}: 5, {m1, 2}: 6},
		},
		{
			name: "zero after a positive contribution keeps the slot",
			setup: func(c *Cache) {
				c.Set(m1, 1, 5)
				c.Set(m1, 1, 0)
			},
			wantItems:  1,
			wantWeight: map[slot]uint64{{m1, 1}: 5},
		},
		{
			name: "absent mask misses while another is stored",
			setup: func(c *Cache) {
				c.Set(m2, 0, 9)
			},
			wantItems:  1,
			wantWeight: map[slot]uint64{{m2, 0}: 9},
			wantAbsent: []slot{{m1, 0}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCache()
			if tt.setup != nil {
				tt.setup(c)
			}
			v := c.Seal() // writers done — barrier point
			assert.Equal(t, tt.wantItems, v.Len())

			for s, want := range tt.wantWeight {
				val, found := v.Get(s.mask)
				require.Truef(t, found, "mask %v missing", s.mask)
				w, ok := val.Weight(s.rep)
				assert.True(t, ok)
				assert.Equal(t, want, w)
			}
			for _, s := range tt.wantAbsent {
				val, found := v.Get(s.mask)
				if !found {
					continue
				}
				_, ok := val.Weight(s.rep)
				assert.Falsef(t, ok, "class %d of %v must miss", s.rep, s.mask)
			}
		})
	}

	t.Run("absent mask gives zero Value", func(t *testing.T) {
		val, found := NewCache().Seal().Get(m1)
		assert.False(t, found)
		assert.Equal(t, Value{}, val)
		_, ok := val.Weight(0)
		assert.False(t, ok, "zero Value answers every class as absent")
	})
}

// All covers the whole table exactly once — no duplicates, no losses — every
// class of every mask appears on its own Entry, Len counts masks and is
// strictly below the delivered class count for a multi-slot table, and a
// repeated walk over the same handle sees the identical set (specs/cache.md).
func TestViewAllCoversTable(t *testing.T) {
	const n = 500
	c := NewCache()
	want := make(map[slot]uint64, 2*n)
	for i := range n {
		c.Set(state.State(i), uint8(i%3), uint64(i+1))
		want[slot{state.State(i), uint8(i % 3)}] = uint64(i + 1)
	}
	c.Set(state.State(7), 9, 100) // second class on an existing mask
	want[slot{state.State(7), 9}] = 100

	v := c.Seal()
	first := walkAll(t, v)
	assert.Equal(t, want, first, "All covers every class once")
	assert.Len(t, first, n+1, "the second class on mask 7 is delivered too")
	assert.Equal(t, n, v.Len(), "Len counts masks; All must not drain the table")
	assert.Less(t, v.Len(), len(first), "multi-slot table: masks < classes")
	assert.Equal(t, first, walkAll(t, v), "repeated walk is identical (data stays)")
}

// Breaking out of an All walk ends it there — a strict prefix shorter than the
// full table, no panic, and the table intact (specs/cache.md: break is not an
// error, the iterator has no error source).
func TestViewAllBreakGivesPrefix(t *testing.T) {
	c := NewCache()
	const n = 100
	for i := range n {
		c.Set(state.State(i), 0, uint64(i+1))
	}

	v := c.Seal()
	seen := 0
	for range v.All(context.Background()) {
		seen++
		if seen == 5 {
			break
		}
	}
	assert.Equal(t, 5, seen, "break ends the walk at that record")
	assert.Equal(t, n, v.Len(), "a partial walk leaves the table intact")
}

// A break in the middle of a multi-class record delivers exactly the leading
// classes of that record's expansion order (the block's live positions in
// insertion order) and none of the rest — the cut inside a record is legal and
// not an error (specs/cache.md).
func TestViewAllBreakInsideRecord(t *testing.T) {
	const m state.State = 0x1234
	classes := 10 // crosses slab capacity boundaries on the way up

	c := NewCache()
	for i := range classes {
		c.Set(m, uint8(i), uint64(i+1))
	}
	v := c.Seal()

	var full []Entry
	for e := range v.All(context.Background()) {
		full = append(full, e)
	}
	require.Len(t, full, classes)

	const cut = 3
	var got []Entry
	for e := range v.All(context.Background()) {
		got = append(got, e)
		if len(got) == cut {
			break
		}
	}
	assert.Equal(t, full[:cut], got, "the walk delivers a strict prefix of the record")
}

// A terminated ctx stops the All walk at a shard boundary: cancel mid-walk
// delivers strictly fewer records than the full table, an already terminated
// ctx delivers none (specs/cache.md).
func TestViewAllCtxCancelStopsWalk(t *testing.T) {
	c := NewCache()
	const n = 2000
	for i := range n {
		c.Set(state.State(i), 0, uint64(i+1))
	}

	tests := []struct {
		name        string
		cancelInRun bool // else cancel before the first iteration
	}{
		{name: "cancelled mid-walk", cancelInRun: true},
		{name: "already cancelled", cancelInRun: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !tt.cancelInRun {
				cancel()
			}

			v := c.Seal()
			seen := 0
			for range v.All(ctx) {
				seen++
				if tt.cancelInRun {
					cancel()
				}
			}
			assert.Less(t, seen, n, "cancelled walk stops before the whole table")
			if tt.cancelInRun {
				assert.Positive(t, seen, "the shard in flight still delivers its records")
			} else {
				assert.Zero(t, seen, "a terminated ctx yields no records")
			}
		})
	}
}

// Sharding invariant (specs/cache.md): the hash is mask-only — rep never
// takes part — so all classes of one mask live in one shard/record, while
// distinct states must still spread across shards.
func TestShardIndexIsMaskOnly(t *testing.T) {
	states := []state.State{0b1, 0b1001, 0xFF00FF, 0x123456789ABCDEF}

	c := NewCache()
	for _, st := range states {
		for rep := range 16 {
			c.Set(st, uint8(rep), 1)
		}
		val, found := c.Seal().Get(st)
		require.True(t, found)
		k, _ := slabParts(val.word)
		assert.Equal(t, 16, k, "one record accumulates all classes of the mask")
	}

	seen := make(map[int]bool)
	for _, st := range states {
		seen[shardIndex(st)] = true
	}
	assert.Greater(t, len(seen), 1, "hash must spread distinct states")
}

// Concurrent Set of overlapping (mask, rep) slots under -race: every class
// written is readable after the barrier with the full summed weight through a
// sealed View (writers-only Mutex; reads exist only post-barrier).
func TestCacheConcurrentSet(t *testing.T) {
	const writers = 8
	const perWriter = 2000

	c := NewCache()
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				c.Set(state.State(i), uint8(w%2), 1) // same slots across writers → sums merge
			}
		})
	}
	wg.Wait()

	v := c.Seal()
	assert.Equal(t, perWriter, v.Len(), "same masks merge across writers")
	for i := range perWriter {
		val, found := v.Get(state.State(i))
		require.True(t, found)
		w0, ok0 := val.Weight(0)
		w1, ok1 := val.Weight(1)
		assert.Equal(t, uint64(writers/2), w0)
		assert.Equal(t, uint64(writers/2), w1)
		assert.True(t, ok0 && ok1)
	}
}

// assertVisible checks that exactly the wanted classes are visible in the
// sealed view (right weight via Get+Weight) and every other probed slot of
// the probe masks misses; Len bounds the table to the visible mask set.
func assertVisible(t *testing.T, v *View, want map[slot]uint64, probes ...state.State) {
	t.Helper()
	masks := make(map[state.State]bool)
	for s := range want {
		val, found := v.Get(s.mask)
		require.Truef(t, found, "visibility of %v", s.mask)
		w, ok := val.Weight(s.rep)
		assert.Truef(t, ok, "class %d of %v", s.rep, s.mask)
		assert.Equalf(t, want[s], w, "weight of %v class %d", s.mask, s.rep)
		masks[s.mask] = true
	}
	for _, m := range probes {
		if masks[m] {
			continue
		}
		_, found := v.Get(m)
		assert.Falsef(t, found, "mask %v must stay invisible", m)
	}
	assert.Equal(t, len(masks), v.Len(), "no records beyond the visible set")
}

// stagedCount is the writer-side state a test may inspect in-package: entries
// still buffered across all baskets (specs/cache.md: before the flush they
// live in the Staging, not in the table).
func stagedCount(s *Staging) int {
	total := 0
	for i := range s.baskets {
		total += len(s.baskets[i])
	}
	return total
}

// Staging buffers contributions until a flush boundary: before it they sit in
// the baskets, after Flush + Seal every emission is visible via View.Get and
// Weight (duplicates of one (mask, rep) inside a batch collapse into the
// slot sum), zero weight is never staged and does not consume the limit,
// auto-flush at the limit loses nothing, and limit < 1 clamps to per-Set
// flushing (specs/cache.md).
func TestStagingFlushBoundaries(t *testing.T) {
	const m1 state.State = 0b1
	const m2 state.State = 0b100

	tests := []struct {
		ops        func(s *Staging)
		afterFlush map[slot]uint64
		name       string
		wantStaged int
		limit      int
	}{
		{
			name:       "batch invisible until explicit flush",
			limit:      100,
			ops:        func(s *Staging) { s.Set(m1, 1, 3); s.Set(m1, 1, 4); s.Set(m2, 1, 5) },
			wantStaged: 3,
			afterFlush: map[slot]uint64{{m1, 1}: 7, {m2, 1}: 5},
		},
		{
			name:       "zero weight is no-op and does not consume the limit",
			limit:      1,
			ops:        func(s *Staging) { s.Set(m1, 1, 0); s.Set(m1, 1, 6) },
			wantStaged: 0, // auto-flushed by the weight-6 Set alone; zero never staged
			afterFlush: map[slot]uint64{{m1, 1}: 6},
		},
		{
			name:       "auto-flush at limit, tail buffered",
			limit:      2,
			ops:        func(s *Staging) { s.Set(m1, 1, 1); s.Set(m2, 1, 2); s.Set(m1, 1, 3) },
			wantStaged: 1,
			afterFlush: map[slot]uint64{{m1, 1}: 4, {m2, 1}: 2},
		},
		{
			name:       "limit clamped to one flushes per Set",
			limit:      0,
			ops:        func(s *Staging) { s.Set(m1, 1, 2) },
			wantStaged: 0,
			afterFlush: map[slot]uint64{{m1, 1}: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCache()
			s := c.NewStaging(tt.limit)
			tt.ops(s)
			assert.Equal(t, tt.wantStaged, stagedCount(s), "unflushed contributions stay in the baskets")

			s.Flush()
			assertVisible(t, c.Seal(), tt.afterFlush, m1, m2)
			s.Flush() // idempotent on drained baskets
			assertVisible(t, c.Seal(), tt.afterFlush, m1, m2)
		})
	}
}

// Concurrent Staging writers (one per goroutine) over overlapping slots under
// -race: after the barrier and a single Seal every class sums across all
// writers — flush boundaries lose nothing.
func TestStagingConcurrentWriters(t *testing.T) {
	const writers = 8
	const perWriter = 1000

	c := NewCache()
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			s := c.NewStaging(64)
			defer s.Flush()
			for i := range perWriter {
				s.Set(state.State(i), 0, 1) // same slots across writers → sums merge
			}
		})
	}
	wg.Wait()

	v := c.Seal()
	assert.Equal(t, perWriter, v.Len())
	for i := range perWriter {
		val, found := v.Get(state.State(i))
		require.True(t, found)
		w, ok := val.Weight(0)
		assert.True(t, ok)
		assert.Equal(t, uint64(writers), w)
	}
}

// Concurrent Views of one sealed table under -race: independent Seal handles,
// parallel Get+Weight and All walks all observe one identical snapshot — reads
// take no locks because no writer exists (specs/cache.md).
func TestViewConcurrentHandlesAgree(t *testing.T) {
	c := NewCache()
	const n = 2000
	for i := range n {
		c.Set(state.State(i), uint8(i%3), uint64(i+1)) // writers done — barrier point
	}

	var mismatches, lostWalks atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		v := c.Seal()
		wg.Go(func() {
			for i := range n {
				val, ok := v.Get(state.State(i))
				if !ok {
					mismatches.Add(1)
					continue
				}
				w, found := val.Weight(uint8(i % 3))
				if !found || w != uint64(i+1) {
					mismatches.Add(1)
				}
			}
		})
		wg.Go(func() {
			for range 50 {
				seen := make(map[slot]uint64, n)
				for e := range v.All(context.Background()) {
					seen[slot{e.Mask, e.Rep}] = e.Weight
				}
				if len(seen) != n || seen[slot{7, 1}] != 8 {
					lostWalks.Add(1)
				}
			}
		})
	}
	wg.Wait()

	assert.Zero(t, mismatches.Load(), "every View agrees with the written weights")
	assert.Zero(t, lostWalks.Load(), "every walk sees the whole snapshot")
	assert.Equal(t, n, c.Seal().Len(), "reads must not mutate the table")
}

// The packed rep:8 | W:56 layout round-trips boundary values within the rep
// contract (< 128) and keeps a weight of exactly wBits width intact
// (specs/cache.md).
func TestPackUnpackRoundTrip(t *testing.T) {
	tests := []struct {
		rep    uint8
		weight uint64
	}{
		{rep: 0, weight: 1},
		{rep: 63, weight: wMask},
		{rep: 127, weight: 1 << (wBits - 1)}, // contract top: bit 63 stays clear
	}

	for i, tt := range tests {
		t.Run(fmt.Sprintf("rep%d_w%d", i, tt.weight), func(t *testing.T) {
			c := NewCache()
			c.Set(state.State(0xF), tt.rep, tt.weight)
			val, found := c.Seal().Get(state.State(0xF))
			require.True(t, found)
			w, ok := val.Weight(tt.rep)
			assert.True(t, ok)
			assert.Equal(t, tt.weight, w)
		})
	}
}

// Value-format encoding (specs/cache.md): capacityFor is 2^⌈log₂ k⌉ across the
// structural domain, slab references round-trip (k | idx), and the two value
// forms never collide — a packed pair keeps bit 63 clear under the rep < 128
// contract while a reference sets it.
func TestValueEncoding(t *testing.T) {
	tests := []struct {
		want int
		k    int
	}{
		{k: 1, want: 1}, {k: 2, want: 2}, {k: 3, want: 4}, {k: 4, want: 4},
		{k: 5, want: 8}, {k: 9, want: 16}, {k: 17, want: 32},
		{k: 33, want: 64}, {k: maxClasses, want: 64},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, capacityFor(tt.k), "capacityFor(%d)", tt.k)
	}

	refs := []struct{ k, idx int }{{2, 0}, {7, 1}, {64, maxClasses}, {64, int(wMask)}}
	for _, tt := range refs {
		k, idx := slabParts(makeRef(tt.k, tt.idx))
		assert.Equal(t, tt.k, k)
		assert.Equal(t, tt.idx, idx)
	}

	assert.Zero(t, pack(127, wMask)&tagBit, "a packed pair keeps the tag bit clear (rep < 128)")
	assert.NotZero(t, makeRef(2, 0)&tagBit, "a slab reference sets the tag bit")
}

// The second class of a mask promotes its value from the inline pair into a
// two-class slab block: before it the stored word carries no tag bit, after it
// both classes read exact sums and All unfolds both entries (specs/cache.md).
func TestValuePromoteToSlab(t *testing.T) {
	const m state.State = 0xABCD
	c := NewCache()
	c.Set(m, 5, 7)

	val, found := c.Seal().Get(m) // writers pause at this in-test barrier point
	require.True(t, found)
	assert.Zero(t, val.word&tagBit, "a lone class stays inline unless it grows")
	w, ok := val.Weight(5)
	require.True(t, ok)
	assert.Equal(t, uint64(7), w)

	c.Set(m, 9, 11)
	v := c.Seal()
	val, found = v.Get(m)
	require.True(t, found)
	assert.NotZero(t, val.word&tagBit, "the second class takes the slab form")
	k, _ := slabParts(val.word)
	assert.Equal(t, 2, k, "both classes live in one block")

	w, ok = val.Weight(9)
	require.True(t, ok)
	assert.Equal(t, uint64(11), w)

	assert.Equal(t, map[slot]uint64{{m, 5}: 7, {m, 9}: 11}, walkAll(t, v))
	assert.Equal(t, 1, v.Len(), "one record per mask")
}

// Growth across every slab capacity boundary (k = 2, 3, 5, 9, 17, 33, … up to
// the structural maximum) through repeated Set and through per-class Staging
// flushes alike: Weight stays exact for every class, All unfolds exactly one
// Entry per class in insertion order — relocations lose, duplicate or reorder
// nothing (specs/cache.md).
func TestGrowthAcrossCapacityBoundaries(t *testing.T) {
	const m state.State = 0xABCD
	tests := []struct {
		sink func(c *Cache) Sink
		name string
	}{
		{name: "direct Set", sink: func(c *Cache) Sink { return c }},
		// limit 1 flushes every buffered class on its own Set, so the staged
		// path replays one batch per class in insertion order.
		{name: "staging batch per class", sink: func(c *Cache) Sink { return c.NewStaging(1) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCache()
			sink := tt.sink(c)
			want := make(map[slot]uint64, maxClasses)
			order := make([]uint8, maxClasses)
			for i := range maxClasses {
				order[i] = uint8(i)
				sink.Set(m, uint8(i), uint64(i+1))
				sink.Set(m, uint8(i), uint64(i+1)) // two contributions per class
				want[slot{m, uint8(i)}] = uint64(2 * (i + 1))
			}

			v := c.Seal()
			assert.Equal(t, 1, v.Len())
			val, found := v.Get(m)
			require.True(t, found)
			k, _ := slabParts(val.word)
			assert.Equal(t, maxClasses, k, "every class grew the same record")

			var gotOrder []uint8
			for e := range v.All(context.Background()) {
				gotOrder = append(gotOrder, e.Rep)
			}
			assert.Equal(t, order, gotOrder, "block positions keep insertion order across relocations")
			assert.Equal(t, want, walkAll(t, v), "every class reads its exact sum")
		})
	}
}

// Relocation hands the vacated block to its capacity-class free basket and the
// interleaved growth of another mask in the same shard takes exactly those
// blocks: the slab stops growing on basket hits, and the previous owner's
// garbage left in a recycled reserve tail is never read — not by Weight, not
// by All (specs/cache.md).
func TestBasketReuseOnRelocation(t *testing.T) {
	m1 := state.State(1)
	m2 := m1 + 1
	for shardIndex(m2) != shardIndex(m1) {
		m2++
	}
	c := NewCache()
	sh := &c.shards[shardIndex(m1)]

	// m1 to five classes: promote (cap 2), relocate to cap 4, grow in place
	// to four, relocate to cap 8 — vacating its cap-2 and cap-4 blocks.
	for rep := range 5 {
		c.Set(m1, uint8(rep), 1)
	}
	assert.Len(t, sh.free[freeClass(2)], 1, "vacated cap-2 block waits in its basket")
	assert.Len(t, sh.free[freeClass(4)], 1, "vacated cap-4 block waits in its basket")

	// m2 interleaves to three classes: promote and one relocation must both
	// hit the baskets instead of extending the slab.
	slabBefore := len(sh.slab)
	for rep := range 3 {
		c.Set(m2, uint8(rep), 10)
	}
	assert.Len(t, sh.slab, slabBefore, "basket hits must not grow the slab")
	assert.Empty(t, sh.free[freeClass(4)], "the cap-4 basket was consumed by m2")

	v := c.Seal()
	val2, found := v.Get(m2)
	require.True(t, found)
	_, ok := val2.Weight(3) // the recycled tail still holds m1's class-3 pair
	assert.Falsef(t, ok, "reserve tail of the previous owner must stay invisible (m2 block)")

	want := make(map[slot]uint64, 8)
	for rep := range 5 {
		want[slot{m1, uint8(rep)}] = 1
	}
	for rep := range 3 {
		want[slot{m2, uint8(rep)}] = 10
	}
	assert.Equal(t, want, walkAll(t, v), "both masks read back exactly after reuse")
	assert.Equal(t, 2, v.Len())
}
