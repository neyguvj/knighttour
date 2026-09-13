package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/path"
	"knighttour/state"
)

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
			val, found := c.Get(tt.query)
			assert.Equal(t, tt.wantVal, val)
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.wantItems, c.ItemsCount())
		})
	}
}

// eachUnion walks c once via Each and folds the delivered records into a map,
// asserting on the way that every record the callback sees is exactly what Get
// returns for its key (specs/cache.md).
func eachUnion(t *testing.T, c *Cache, workers int) map[path.Path]uint64 {
	t.Helper()
	m := make(map[path.Path]uint64)
	var mu sync.Mutex
	err := c.Each(context.Background(), workers, func(_ context.Context, p path.Path, w uint64) error {
		stored, found := c.Get(p) // shared RLock: Get works from inside the walk
		assert.Truef(t, found, "Get lost record %v", p)
		assert.Equal(t, w, stored)
		mu.Lock()
		defer mu.Unlock()
		_, dup := m[p]
		assert.False(t, dup, "key emitted by two shards")
		m[p] = w
		return nil
	})
	require.NoError(t, err)
	return m
}

// Each covers the whole table exactly once — no duplicates, no losses — with
// workers clamped (0), a single worker and many, and leaves every record in
// place: Get still sees it and a repeated walk is identical (specs/cache.md).
func TestCacheEachCoversTable(t *testing.T) {
	const n = 500
	tests := []struct {
		name    string
		workers int
	}{
		{name: "zero workers clamped", workers: 0},
		{name: "single worker", workers: 1},
		{name: "many workers", workers: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCache()
			for i := range n {
				c.Set(path.New(state.State(i), i%17), uint64(i+1))
			}

			first := eachUnion(t, c, tt.workers)
			assert.Len(t, first, n, "Each covers the table once")
			assert.Equal(t, n, c.ItemsCount(), "Each must not drain the table")
			assert.Equal(t, first, eachUnion(t, c, tt.workers), "repeated walk is identical (data stays)")
		})
	}
}

// Cancelling ctx stops the walk before the next shard: with a single worker at
// most the shard in flight finishes, so strictly less than the full table is
// delivered and Each returns ctx.Err() (specs/cache.md).
func TestCacheEachCtxCancelStopsWalk(t *testing.T) {
	c := NewCache()
	const n = 2000
	for i := range n {
		c.Set(path.New(state.State(i), 0), uint64(i+1))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen int
	err := c.Each(ctx, 1, func(_ context.Context, _ path.Path, _ uint64) error {
		seen++
		if seen == 1 {
			cancel()
		}
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Positive(t, seen, "the shard in flight still delivers its records")
	assert.Less(t, seen, n, "cancelled walk stops before the whole table")
}

// The first error from f is returned by Each and stops scheduling new shards:
// with a single worker the walk ends at exactly that record (specs/cache.md).
func TestCacheEachStopsOnFirstError(t *testing.T) {
	c := NewCache()
	const n = 100
	for i := range n {
		c.Set(path.New(state.State(i), 0), uint64(i+1))
	}

	wantErr := errors.New("callback failed")
	var seen int
	err := c.Each(context.Background(), 1, func(_ context.Context, _ path.Path, _ uint64) error {
		seen++
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, 1, seen, "the first error stops the walk")
}

// walkOnce runs one full Each walk of c and reports how many records of st it
// delivered plus the first callback error. A weight mismatch is surfaced as an
// error so Each aborts that shard — safe because every reader drives its own
// walk with a fresh context.
func walkOnce(c *Cache, st state.State) (int, error) {
	seen := 0
	err := c.Each(context.Background(), 4, func(_ context.Context, p path.Path, w uint64) error {
		if p.State() != st {
			return nil
		}
		if want := uint64(p.End() + 1); w != want {
			return fmt.Errorf("weight for end %d: got %d want %d", p.End(), w, want)
		}
		seen++
		return nil
	})
	return seen, err
}

// Concurrent Get and Each of one shard under -race: readers share the RLock
// (no writers in the count phase), so every walk is consistent with the live
// table and nothing is mutated. All keys share one State → one shard: walks
// and Gets contend on exactly one read lock (specs/cache.md). n = 256 is the
// whole uint8 end range — the distinct keys one State can hold.
func TestCacheConcurrentGetAndEach(t *testing.T) {
	c := NewCache()
	const n = 256
	const st = state.State(0x5F00FF00)
	for e := range n {
		c.Set(path.New(st, e), uint64(e+1))
	}

	var missing, badWalks atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for i := range n {
				if _, found := c.Get(path.New(st, i)); !found {
					missing.Add(1)
				}
			}
		})
		wg.Go(func() {
			for range 50 {
				seen, err := walkOnce(c, st)
				if err != nil || seen != n {
					badWalks.Add(1)
				}
			}
		})
	}
	wg.Wait()

	assert.Zero(t, missing.Load(), "Get never loses a record during concurrent walks")
	assert.Zero(t, badWalks.Load(), "every walk sees all records with correct weights")
	assert.Equal(t, n, c.ItemsCount(), "readers must not mutate the table")
}

// Shared sharding invariant (specs/cache.md): all ends of one State hash to
// one shard — for the Cache this only spreads contention, but the helper is
// shared with the Accumulator and must stay State-only.
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

// Concurrent Set/Get of disjoint key ranges under -race: every record written
// is readable afterwards with the full summed weight.
func TestCacheConcurrentSetGet(t *testing.T) {
	const writers = 8
	const perWriter = 2000

	c := NewCache()
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for i := range perWriter {
				k := path.New(state.State(i), 0) // same keys across writers → sums merge
				c.Set(k, 1)
				c.Get(k) // concurrent readers over live writers
			}
		})
	}
	wg.Wait()

	assert.Equal(t, perWriter, c.ItemsCount(), "same keys merge across writers")
	for i := range perWriter {
		weight, found := c.Get(path.New(state.State(i), 0))
		assert.True(t, found)
		assert.Equal(t, uint64(writers), weight)
	}
}
