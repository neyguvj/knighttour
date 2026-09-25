package cache

import (
	"context"
	"iter"
	"sync"

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

// cacheShard is one Cache shard: its map plus the writers-only Mutex — Set and
// Staging.Flush are the only lock takers; sealed reads take none (plan 16).
type cacheShard struct {
	data map[path.Path]uint64
	mu   sync.Mutex
}

// Cache is the counting pipeline's single sharded "canonical key → Σ orbitSize"
// table (specs/cache.md, ADR-011/ADR-018): one type plays both pipeline roles —
// the short-lived gen-A intermediate and the task-cache that stays alive
// through the count phase. It follows the write → seal → read protocol: writers
// contribute via Set/Staging until the calling contour stops them (a barrier
// giving happens-before) and calls Seal; from then on every read goes through
// the lock-free *View handle and no writer exists (specs/cache.md). Keys are
// D4-canonical prefixes (state, end); the writer canonicalizes — this type
// knows nothing about symmetries. Zeros are never stored: Set with a zero
// weight is a no-op. There is no per-shard draining and no copying snapshot.
type Cache struct {
	shards [numShards]cacheShard
}

// NewCache returns an empty table (128 shards of map[path.Path]uint64 under
// Mutex, hashed by State via shardIndex).
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
// errgroup.Wait or a final Staging.Flush) establishes happens-before with every
// write the View will observe. Repeated Seal yields independent handles over
// the same table; no writer may exist while any View lives (specs/cache.md).
func (c *Cache) Seal() *View { return &View{cache: c} }

// Get looks the key up without locking; ok is false for an absent key.
func (v *View) Get(p path.Path) (uint64, bool) {
	sh := &v.cache.shards[shardIndex(p)]
	weight, found := sh.data[p]
	return weight, found
}

// Len is the number of stored records; constant after Seal because no writer
// exists any more — shards are summed without locking.
func (v *View) Len() int {
	total := 0
	for i := range v.cache.shards {
		total += len(v.cache.shards[i].data)
	}
	return total
}

// All is the lock-free pull-walk over every record of the sealed table, with
// no copying (specs/cache.md): entries come straight out of the live maps
// under Seal's no-writers precondition. Order — shard 0..127, Go map order
// within a shard; ctx is checked at each shard boundary, a terminated ctx
// ends the walk there (a consumer gets a prefix). Breaking out of the range
// stops the walk early and is not an error; the iterator has no error source.
func (v *View) All(ctx context.Context) iter.Seq[Entry] {
	return func(yield func(Entry) bool) {
		for i := range v.cache.shards {
			if ctx.Err() != nil {
				return
			}
			for p, w := range v.cache.shards[i].data {
				if !yield(Entry{Path: p, Weight: w}) {
					return
				}
			}
		}
	}
}
