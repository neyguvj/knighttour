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
	canonicalPath := c.symmetry.CanonicalizePath(p)
	shardIdx := c.getShardKey(canonicalPath)
	c.shards[shardIdx].RLock()
	defer c.shards[shardIdx].RUnlock()
	if count, found := c.shards[shardIdx].data[canonicalPath]; found {
		return count, true
	}
	return 0, false
}

func (c *Cache) Set(p path.Path, val int) {
	if val == 0 {
		return
	}
	canonicalPath := c.symmetry.CanonicalizePath(p)
	shardIdx := c.getShardKey(canonicalPath)
	c.shards[shardIdx].Lock()
	defer c.shards[shardIdx].Unlock()
	c.shards[shardIdx].data[canonicalPath] += val
}

func (c *Cache) Has(p path.Path) bool {
	canonicalPath := c.symmetry.CanonicalizePath(p)
	shardIdx := c.getShardKey(canonicalPath)
	c.shards[shardIdx].RLock()
	defer c.shards[shardIdx].RUnlock()
	_, found := c.shards[shardIdx].data[canonicalPath]
	return found
}

func (c *Cache) Delete(p path.Path) {
	canonicalPath := c.symmetry.CanonicalizePath(p)
	shardIdx := c.getShardKey(canonicalPath)
	c.shards[shardIdx].Lock()
	defer c.shards[shardIdx].Unlock()
	delete(c.shards[shardIdx].data, canonicalPath)
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
