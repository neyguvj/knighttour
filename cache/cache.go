package cache

import (
	"context"
	"sync"

	"golang.org/x/sync/errgroup"

	"knighttour/path"
)

// numShards is the shard count of the table; it must stay 1<<7 to match the
// shift in shardIndex.
const numShards = 128

// Entry is one table record: a canonical key and its accumulated weight — the
// worklist element phase B draws from the gen-A intermediate table
// (specs/cache.md).
type Entry struct {
	Path   path.Path
	Weight uint64
}

// shardIndex hashes State only, so every end of one mask lands in the same
// shard — the key's ends are never split across shards (specs/cache.md,
// ADR-004); for the task-cache it merely spreads contention.
// Allocation-free: golden-ratio multiply, take the high bits. numShards must
// stay 1<<7 to match the shift.
func shardIndex(p path.Path) int {
	h := uint64(p.State()) * 0x9E3779B97F4A7C15
	return int(h >> (64 - 7)) // numShards = 128
}

// cacheShard is one Cache shard: its map plus the RWMutex that lets Get read
// concurrently with live Set writers.
type cacheShard struct {
	data map[path.Path]uint64
	mu   sync.RWMutex
}

// Cache is the counting pipeline's single sharded "canonical key → Σ orbitSize"
// table (specs/cache.md, ADR-011/ADR-018): one type plays both pipeline roles —
// the short-lived gen-A intermediate and the task-cache that stays alive
// through the count phase and is read concurrently via Get while whole entries
// are being written. Keys are D4-canonical prefixes (state, end); the writer
// canonicalizes — this type knows nothing about symmetries. Zeros are never
// stored: Set with a zero weight is a no-op. Reads are live only — Get, or a
// direct walk under shard read locks via Each (plan 09); there is no per-shard
// draining and no copying snapshot.
type Cache struct {
	shards [numShards]cacheShard
}

// NewCache returns an empty table (128 shards of map[path.Path]uint64 under
// RWMutex, hashed by State via shardIndex).
func NewCache() *Cache {
	c := &Cache{}
	for i := range c.shards {
		c.shards[i].data = make(map[path.Path]uint64)
	}
	return c
}

// Set accumulates one contribution: data[key] += weight. A zero weight must
// not create an entry (specs/cache.md: no zeros in the table).
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

// Each dispatches the count phase over every stored record without copying the
// table (specs/cache.md, plan 09): one goroutine per shard, at most workers at
// a time; workers < 1 clamps to the shard count (full parallelism — there is
// never more to run). Per shard: ctx check → RLock → f for each record →
// RUnlock. Data is NOT drained — Get keeps seeing every shard through the
// whole walk, and dispatch allocates nothing (peak is the worker stacks). f
// runs under the shard's read lock, legal only while no writer exists (phase
// invariant); the shared RLock stays concurrent with Get of any shard,
// including its own. The first error from f stops scheduling new shards and is
// returned; a cancelled ctx ends the walk before the next shard and Each
// returns ctx.Err().
func (c *Cache) Each(ctx context.Context, workers int, f func(ctx context.Context, p path.Path, weight uint64) error) error {
	// SetLimit(0) parks the first Go forever (zero-capacity semaphore); only
	// negative limits are unbounded in errgroup. Clamp explicitly instead —
	// more than one goroutine per shard would idle anyway.
	if workers < 1 {
		workers = numShards
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for i := range c.shards {
		sh := &c.shards[i]
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			return eachShard(sh, gctx, f)
		})
	}
	return g.Wait()
}

// eachShard calls f for every record of one shard under its read lock; the
// first non-nil f result aborts the rest of that shard.
func eachShard(sh *cacheShard, ctx context.Context, f func(ctx context.Context, p path.Path, weight uint64) error) error {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	for p, w := range sh.data {
		if err := f(ctx, p, w); err != nil {
			return err
		}
	}
	return nil
}
