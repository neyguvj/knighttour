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

// Sink is the write side of the table: an additive contributor of weighted
// keys. Both *Cache (direct write) and *Staging (batched write, plan 15)
// implement it, so generation descents do not care which one they emit into.
type Sink interface {
	// Set adds one contribution to the table; a zero weight is a no-op.
	Set(p path.Path, weight uint64)
}

var (
	_ Sink = (*Cache)(nil)
	_ Sink = (*Staging)(nil)
)

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

// Staging is one goroutine's batched writer into a Cache (plan 15): emissions
// land in per-shard baskets (append, no hashing — the shard index is a
// multiply) and only become visible under one Lock per touched shard at
// Flush. It exists to cut the lock/wake churn of per-leaf Set on hot tables;
// it stores no data of its own beyond the pending basket. Not thread-safe:
// exactly one goroutine owns a Staging, concurrent Stagings and Get are safe.
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

// Set buffers one contribution in its shard's basket; a zero weight is a
// no-op and does not consume the limit. Auto-flushes at the configured limit.
func (s *Staging) Set(p path.Path, weight uint64) {
	if weight == 0 {
		return
	}
	i := shardIndex(p)
	s.baskets[i] = append(s.baskets[i], Entry{Path: p, Weight: weight})
	s.pending++
	if s.pending == s.limit {
		s.Flush()
	}
}

// Flush folds every non-empty basket into its shard under that shard's Lock —
// duplicate keys within one batch collapse into a single addition (additive
// merge, commutative like Set, so the table at the phase barrier is identical
// to direct writes). Baskets keep capacity for reuse; pending resets.
func (s *Staging) Flush() {
	for i := range s.baskets {
		basket := s.baskets[i]
		if len(basket) == 0 {
			continue
		}
		sh := &s.cache.shards[i]
		sh.mu.Lock()
		for _, e := range basket {
			sh.data[e.Path] += e.Weight
		}
		sh.mu.Unlock()
		s.baskets[i] = basket[:0] // reuse capacity; Entry is pointer-free, no clearing needed
	}
	s.pending = 0
}

// Reader is a lock-free read handle of a quiescent Cache (plan 15): direct
// map lookups without any locking. Its precondition belongs to the calling
// pipeline, not to this type — it must be created only once every writer is
// done (the generation phase barrier) and used while no writer exists; Go
// maps are safe for concurrent readers, so many Readers coexist freely.
// Violating the precondition is a data race by contract — the same class as
// Each's "no writers" rule.
type Reader struct {
	cache *Cache
}

// Reader returns a lock-free read handle of c. Creating it after a barrier to
// all writers (e.g. errgroup.Wait or ItemsCount) establishes happens-before
// with every write the Reader will observe.
func (c *Cache) Reader() *Reader { return &Reader{cache: c} }

// Get looks the key up without locking; ok is false for an absent key.
func (r *Reader) Get(p path.Path) (uint64, bool) {
	sh := &r.cache.shards[shardIndex(p)]
	weight, found := sh.data[p]
	return weight, found
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
