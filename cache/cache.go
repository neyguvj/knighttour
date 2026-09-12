package cache

import (
	"sync"

	"knighttour/path"
)

// cacheShard is one Cache shard: its map plus the RWMutex that lets Get read
// concurrently with live Set writers.
type cacheShard struct {
	data map[path.Path]uint64
	mu   sync.RWMutex
}

// Cache is the task-cache of reversal mode (specs/cache.md, ADR-011): an
// additive "canonical key → Σ orbitSize" table that stays alive through the
// count phase and is read concurrently via Get while whole entries are being
// written. Keys are D4-canonical prefixes (state, end); the writer canonicalizes —
// this type knows nothing about symmetries. Zeros are never stored: Set with a
// zero weight is a no-op. Unlike the Accumulator it is never drained per shard;
// the count phase reads it lazily via NumShards/SnapshotShard (ADR-012).
type Cache struct {
	shards [numShards]cacheShard
}

// NewCache returns an empty task-cache (128 shards of map[path.Path]uint64
// under RWMutex, hashed by State via the shared shardIndex).
func NewCache() *Cache {
	c := &Cache{}
	for i := range c.shards {
		c.shards[i].data = make(map[path.Path]uint64)
	}
	return c
}

// Set accumulates one contribution: data[key] += weight. A zero weight must
// not create an entry (specs/cache.md: no zeros in either table).
func (c *Cache) Set(p path.Path, weight uint64) {
	if weight == 0 {
		return
	}
	sh := &c.shards[shardIndex(p)]
	sh.mu.Lock()
	sh.data[p] += weight
	sh.mu.Unlock()
}

// Get reads a key concurrently with live writers (RLock). ok is false for an
// absent key; misses are legal mid-run — counting correctness relies on the
// phase barrier in the caller, not on visibility here.
func (c *Cache) Get(p path.Path) (uint64, bool) {
	sh := &c.shards[shardIndex(p)]
	sh.mu.RLock()
	weight, found := sh.data[p]
	sh.mu.RUnlock()
	return weight, found
}

// ItemsCount is the number of stored records; each shard is counted under its
// own read lock.
func (c *Cache) ItemsCount() int {
	total := 0
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		total += len(sh.data)
		sh.mu.RUnlock()
	}
	return total
}

// NumShards returns the number of independently snapshot-able shards (the same
// count as the Accumulator's), bounding the shard-index cursor of the count
// phase (specs/cache.md, ADR-012).
func (c *Cache) NumShards() int { return numShards }

// SnapshotShard copies shard i under a read lock and releases it before
// returning. Unlike Accumulator.DrainShard the data stays in place: Get must
// still see every shard through the whole count phase, so a repeated snapshot
// of the same shard is legal and concurrent with Get (shared RLock — there are
// no writers after the generation barrier). Dispatch peak memory is one shard
// per worker, not the whole table.
func (c *Cache) SnapshotShard(i int) []Entry {
	sh := &c.shards[i]
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	out := make([]Entry, 0, len(sh.data))
	for p, w := range sh.data {
		out = append(out, Entry{Path: p, Weight: w})
	}
	return out
}
