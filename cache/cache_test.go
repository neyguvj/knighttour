package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/path"
	"knighttour/state"
)

// TestCacheSetGetAndZeroWeight pins Set's additive semantics and the sealed
// View's hit/miss/Len over them (specs/cache.md): reads exist only through a
// handle taken after the writers are done.
func TestCacheSetGetAndZeroWeight(t *testing.T) {
	tests := []struct {
		setup     func(c *Cache)
		name      string
		query     path.Path
		wantVal   uint64
		wantItems int
		wantFound bool
	}{
		{
			name:      "miss on empty table",
			query:     path.New(state.State(0b101), 1),
			wantVal:   0,
			wantFound: false,
			wantItems: 0,
		},
		{
			name:      "zero weight stores nothing",
			setup:     func(c *Cache) { c.Set(path.New(state.State(0b101), 1), 0) },
			query:     path.New(state.State(0b101), 1),
			wantFound: false,
			wantItems: 0,
		},
		{
			name: "Set sums weights under one key",
			setup: func(c *Cache) {
				c.Set(path.New(state.State(0b101), 1), 3)
				c.Set(path.New(state.State(0b101), 1), 4)
			},
			query:     path.New(state.State(0b101), 1),
			wantVal:   7,
			wantFound: true,
			wantItems: 1,
		},
		{
			name: "ends of one state are distinct keys",
			setup: func(c *Cache) {
				c.Set(path.New(state.State(0b101), 1), 5)
				c.Set(path.New(state.State(0b101), 2), 6)
			},
			query:     path.New(state.State(0b101), 2),
			wantVal:   6,
			wantFound: true,
			wantItems: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCache()
			if tt.setup != nil {
				tt.setup(c)
			}
			v := c.Seal() // writers done — barrier point
			val, found := v.Get(tt.query)
			assert.Equal(t, tt.wantVal, val)
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.wantItems, v.Len())
		})
	}
}

// walkAll collects one full All walk of the sealed table into a map and
// asserts on the way that every delivered record matches Get for its key and
// no key is emitted twice (specs/cache.md).
func walkAll(t *testing.T, v *View) map[path.Path]uint64 {
	t.Helper()
	m := make(map[path.Path]uint64, v.Len())
	for e := range v.All(context.Background()) {
		stored, found := v.Get(e.Path)
		assert.Truef(t, found, "Get lost record %v", e.Path)
		assert.Equal(t, e.Weight, stored)
		_, dup := m[e.Path]
		assert.False(t, dup, "key emitted twice")
		m[e.Path] = e.Weight
	}
	return m
}

// All covers the whole table exactly once — no duplicates, no losses — Len
// equals the delivered count, and a repeated walk over the same handle sees
// the identical set: the walk never drains or mutates (specs/cache.md).
func TestViewAllCoversTable(t *testing.T) {
	const n = 500
	c := NewCache()
	for i := range n {
		c.Set(path.New(state.State(i), i%17), uint64(i+1))
	}

	v := c.Seal()
	first := walkAll(t, v)
	assert.Len(t, first, n, "All covers the table once")
	assert.Equal(t, n, v.Len(), "All must not drain the table")
	assert.Equal(t, first, walkAll(t, v), "repeated walk is identical (data stays)")
}

// Breaking out of an All walk ends it there — a strict prefix shorter than the
// full table, no panic, and the table intact (specs/cache.md: break is not an
// error, the iterator has no error source).
func TestViewAllBreakGivesPrefix(t *testing.T) {
	c := NewCache()
	const n = 100
	for i := range n {
		c.Set(path.New(state.State(i), 0), uint64(i+1))
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

// A terminated ctx stops the All walk at a shard boundary: cancel mid-walk
// delivers strictly fewer records than the full table, an already terminated
// ctx delivers none (specs/cache.md).
func TestViewAllCtxCancelStopsWalk(t *testing.T) {
	c := NewCache()
	const n = 2000
	for i := range n {
		c.Set(path.New(state.State(i), 0), uint64(i+1))
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

// Sharding invariant (specs/cache.md): all ends of one State hash to one
// shard — the key's ends are never split, so shardIndex must stay State-only.
func TestShardIndexIsStateOnly(t *testing.T) {
	states := []state.State{0b1, 0b1001, 0xFF00FF, 0x123456789ABCDEF}
	for _, st := range states {
		idx := shardIndex(path.New(st, 0))
		for e := 1; e < 64; e++ {
			assert.Equal(t, idx, shardIndex(path.New(st, e)), "state %b spread across shards", st)
		}
	}

	seen := make(map[int]bool)
	for _, st := range states {
		seen[shardIndex(path.New(st, 0))] = true
	}
	assert.Greater(t, len(seen), 1, "hash must spread distinct states")
}

// Concurrent Set of overlapping keys under -race: every record written is
// readable after the barrier with the full summed weight through a sealed
// View (writers-only Mutex; reads exist only post-barrier).
func TestCacheConcurrentSet(t *testing.T) {
	const writers = 8
	const perWriter = 2000

	c := NewCache()
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for i := range perWriter {
				c.Set(path.New(state.State(i), 0), 1) // same keys across writers → sums merge
			}
		})
	}
	wg.Wait()

	v := c.Seal()
	assert.Equal(t, perWriter, v.Len(), "same keys merge across writers")
	for i := range perWriter {
		weight, found := v.Get(path.New(state.State(i), 0))
		require.True(t, found)
		assert.Equal(t, uint64(writers), weight)
	}
}

// assertVisible checks that exactly want[path] entries of keys are visible in
// the sealed view (right weight, found) and every other key misses; Len bounds
// the table to the visible set.
func assertVisible(t *testing.T, v *View, want map[path.Path]uint64, keys ...path.Path) {
	t.Helper()
	stored := 0
	for _, k := range keys {
		w, found := v.Get(k)
		wantW, wantFound := want[k]
		assert.Equalf(t, wantFound, found, "visibility of %v", k)
		if wantFound {
			assert.Equalf(t, wantW, w, "weight of %v", k)
			stored++
		}
	}
	assert.Equal(t, stored, v.Len(), "no records beyond the visible set")
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
// the baskets, after Flush + Seal every emission is visible via View.Get
// (duplicates inside one batch collapse), zero weight is never staged and does
// not consume the limit, auto-flush at the limit loses nothing, and limit < 1
// clamps to per-Set flushing (specs/cache.md).
func TestStagingFlushBoundaries(t *testing.T) {
	k1 := path.New(state.State(0b1), 1)
	k2 := path.New(state.State(0b100), 2)

	tests := []struct {
		ops        func(s *Staging)
		afterFlush map[path.Path]uint64
		name       string
		wantStaged int
		limit      int
	}{
		{
			name:       "batch invisible until explicit flush",
			limit:      100,
			ops:        func(s *Staging) { s.Set(k1, 3); s.Set(k1, 4); s.Set(k2, 5) },
			wantStaged: 3,
			afterFlush: map[path.Path]uint64{k1: 7, k2: 5},
		},
		{
			name:       "zero weight is no-op and does not consume the limit",
			limit:      1,
			ops:        func(s *Staging) { s.Set(k1, 0); s.Set(k1, 6) },
			wantStaged: 0, // auto-flushed by the weight-6 Set alone; zero never staged
			afterFlush: map[path.Path]uint64{k1: 6},
		},
		{
			name:       "auto-flush at limit, tail buffered",
			limit:      2,
			ops:        func(s *Staging) { s.Set(k1, 1); s.Set(k2, 2); s.Set(k1, 3) },
			wantStaged: 1,
			afterFlush: map[path.Path]uint64{k1: 4, k2: 2},
		},
		{
			name:       "limit clamped to one flushes per Set",
			limit:      0,
			ops:        func(s *Staging) { s.Set(k1, 2) },
			wantStaged: 0,
			afterFlush: map[path.Path]uint64{k1: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCache()
			s := c.NewStaging(tt.limit)
			tt.ops(s)
			assert.Equal(t, tt.wantStaged, stagedCount(s), "unflushed contributions stay in the baskets")

			s.Flush()
			assertVisible(t, c.Seal(), tt.afterFlush, k1, k2)
			s.Flush() // idempotent on drained baskets
			assertVisible(t, c.Seal(), tt.afterFlush, k1, k2)
		})
	}
}

// Concurrent Staging writers (one per goroutine) over overlapping keys under
// -race: after the barrier and a single Seal every key sums across all
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
				s.Set(path.New(state.State(i), 0), 1) // same keys across writers → sums merge
			}
		})
	}
	wg.Wait()

	v := c.Seal()
	assert.Equal(t, perWriter, v.Len())
	for i := range perWriter {
		weight, found := v.Get(path.New(state.State(i), 0))
		require.True(t, found)
		assert.Equal(t, uint64(writers), weight)
	}
}

// Concurrent Views of one sealed table under -race: independent Seal handles,
// parallel Get and All walks all observe one identical snapshot — reads take
// no locks because no writer exists (specs/cache.md).
func TestViewConcurrentHandlesAgree(t *testing.T) {
	c := NewCache()
	const n = 2000
	for i := range n {
		c.Set(path.New(state.State(i), i%3), uint64(i+1)) // writers done — barrier point
	}

	var mismatches, lostWalks atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		v := c.Seal()
		wg.Go(func() {
			for i := range n {
				gotW, gotOK := v.Get(path.New(state.State(i), i%3))
				if !gotOK || gotW != uint64(i+1) {
					mismatches.Add(1)
				}
			}
		})
		wg.Go(func() {
			for range 50 {
				seen := make(map[path.Path]uint64, n)
				for e := range v.All(context.Background()) {
					seen[e.Path] = e.Weight
				}
				if len(seen) != n || seen[path.New(state.State(7), 1)] != 8 {
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
