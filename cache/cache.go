package cache

import (
	"context"
	"sync"

	"knighttour/path"
	"knighttour/symmetry"

	"golang.org/x/sync/errgroup"
)

const numShards = 64

type shard struct {
	data map[path.Path]int
	sync.RWMutex
}

// Cache stores search subtasks keyed by the canonical (state, end) pair.
// Values are aggregated weights: sum of count*orbitSize over all prefixes
// that merge into the same key. Total contribution of an entry is
// completions(key) * weight.
type Cache struct {
	symmetry *symmetry.Symmetry
	shards   [numShards]shard
}

func NewCache(sym *symmetry.Symmetry) *Cache {
	c := &Cache{
		symmetry: sym,
	}
	for i := range c.shards {
		c.shards[i].data = make(map[path.Path]int)
	}
	return c
}

func (c *Cache) getShardKey(p path.Path) int {
	// Бесаллокационный хэш: умножение на золотое сечение, старшие биты.
	h := uint64(p.State())*0x9E3779B97F4A7C15 + uint64(p.End())*0xC2B2AE3D27D4EB4F
	return int(h >> (64 - 6)) // numShards = 64
}

func (c *Cache) Get(p path.Path) (int, bool) {
	canonical := c.symmetry.Canonicalize(p.State(), p.End())
	shardIdx := c.getShardKey(canonical)
	c.shards[shardIdx].RLock()
	defer c.shards[shardIdx].RUnlock()
	if weight, found := c.shards[shardIdx].data[canonical]; found {
		return weight, true
	}
	return 0, false
}

// GetCanonical is Get for a key that is already canonical: no re-canonicalization.
// Used by the searcher reversal early stop, which obtains the canonical key and
// its orbit size in one symmetry pass.
func (c *Cache) GetCanonical(p path.Path) (int, bool) {
	shardIdx := c.getShardKey(p)
	c.shards[shardIdx].RLock()
	defer c.shards[shardIdx].RUnlock()
	weight, found := c.shards[shardIdx].data[p]
	return weight, found
}

func (c *Cache) Set(p path.Path, weight int) {
	if weight == 0 {
		return
	}
	canonical := c.symmetry.Canonicalize(p.State(), p.End())
	shardIdx := c.getShardKey(canonical)
	c.shards[shardIdx].Lock()
	defer c.shards[shardIdx].Unlock()
	c.shards[shardIdx].data[canonical] += weight
}

func (c *Cache) Has(p path.Path) bool {
	canonical := c.symmetry.Canonicalize(p.State(), p.End())
	shardIdx := c.getShardKey(canonical)
	c.shards[shardIdx].RLock()
	defer c.shards[shardIdx].RUnlock()
	_, found := c.shards[shardIdx].data[canonical]
	return found
}

func (c *Cache) Delete(p path.Path) {
	canonical := c.symmetry.Canonicalize(p.State(), p.End())
	shardIdx := c.getShardKey(canonical)
	c.shards[shardIdx].Lock()
	defer c.shards[shardIdx].Unlock()
	delete(c.shards[shardIdx].data, canonical)
}

func (c *Cache) Clear() {
	for i := range c.shards {
		c.shards[i].Lock()
		c.shards[i].data = make(map[path.Path]int)
		c.shards[i].Unlock()
	}
}

func (c *Cache) ItemsCount() int {
	total := 0
	for i := range c.shards {
		c.shards[i].RLock()
		total += len(c.shards[i].data)
		c.shards[i].RUnlock()
	}
	return total
}

// Entry is a snapshot record: canonical key plus its aggregated weight.
type Entry struct {
	Path   path.Path
	Weight int
}

// Entries takes a sequential snapshot of all records. Each shard is copied
// under RLock and unlocked before moving to the next one, so writers are
// never blocked for the whole traversal. Used by two-phase subtask generation,
// which needs a work list instead of a callback under a locked shard.
func (c *Cache) Entries() []Entry {
	total := c.ItemsCount()
	entries := make([]Entry, 0, total)
	for i := range c.shards {
		c.shards[i].RLock()
		for p, w := range c.shards[i].data {
			entries = append(entries, Entry{Path: p, Weight: w})
		}
		c.shards[i].RUnlock()
	}
	return entries
}

func (c *Cache) Each(ctx context.Context, workers int, f func(ctx context.Context, p path.Path, count int)) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for i := range c.shards {
		g.Go(func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			c.shards[i].RLock()
			for path, count := range c.shards[i].data {
				f(ctx, path, count)
			}
			c.shards[i].RUnlock()
			return nil
		})
	}
	_ = g.Wait()
}
