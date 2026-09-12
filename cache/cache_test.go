package cache

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

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

// SnapshotShard across NumShards covers the table exactly once and leaves it
// alive: every record stays readable via Get, a repeated union is identical,
// and no key is emitted by two shards (specs/cache.md).
func TestCacheSnapshotShardCoversTable(t *testing.T) {
	c := NewCache()
	const n = 500
	for i := range n {
		c.Set(path.New(state.State(i), i%17), uint64(i+1))
	}

	union := func() map[path.Path]uint64 {
		m := make(map[path.Path]uint64, n)
		for i := range c.NumShards() {
			for _, e := range c.SnapshotShard(i) {
				_, dup := m[e.Path]
				assert.False(t, dup, "key emitted by two shards")
				m[e.Path] = e.Weight
			}
		}
		return m
	}

	first := union()
	assert.Len(t, first, n, "shard snapshots cover the table once")
	assert.Equal(t, n, c.ItemsCount(), "SnapshotShard must not drain the table")
	assert.Equal(t, first, union(), "repeated snapshot is identical (data stays)")

	for i := range n {
		weight, found := c.Get(path.New(state.State(i), i%17))
		assert.True(t, found, "Get sees the record after its shard was snapshotted")
		assert.Equal(t, uint64(i+1), weight)
	}
}

// Concurrent Get and SnapshotShard of one shard under -race: readers share the
// RLock (no writers in the count phase), so every snapshot is consistent with
// the live table and nothing is mutated.
func TestCacheConcurrentGetAndSnapshotShard(t *testing.T) {
	c := NewCache()
	const n = 2000
	for i := range n {
		c.Set(path.New(state.State(i), 0), uint64(i+1))
	}
	shard := shardIndex(path.New(state.State(0), 0))

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for i := range n {
				if _, found := c.Get(path.New(state.State(i), 0)); !found {
					t.Error("record missing during concurrent snapshot")
					return
				}
			}
		})
		wg.Go(func() {
			for range 50 {
				if len(c.SnapshotShard(shard)) == 0 {
					t.Error("shard snapshot empty while the table is populated")
					return
				}
			}
		})
	}
	wg.Wait()

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
