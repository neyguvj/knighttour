package cache

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"sync"

	"knighttour/path"
	"knighttour/symmetry"

	"golang.org/x/sync/errgroup"
)

const numShards = 64

type shard struct {
	sync.RWMutex
	data map[path.Path]int
}

type Cache struct {
	shards   [numShards]shard
	symmetry *symmetry.Symmetry
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
	h := fnv.New64a()
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(p.State()))
	h.Write(buf)
	return int(h.Sum64() % numShards)
}

func (c *Cache) Get(path path.Path) (int, bool) {
	canonicalPath := c.symmetry.CanonicalizePath(path)
	shardIdx := c.getShardKey(canonicalPath)
	c.shards[shardIdx].RLock()
	defer c.shards[shardIdx].RUnlock()
	if count, found := c.shards[shardIdx].data[canonicalPath]; found {
		return count, true
	}
	return 0, false
}

func (c *Cache) Set(path path.Path, val int) {
	if val == 0 {
		return
	}
	canonicalPath := c.symmetry.CanonicalizePath(path)
	shardIdx := c.getShardKey(canonicalPath)
	c.shards[shardIdx].Lock()
	defer c.shards[shardIdx].Unlock()
	c.shards[shardIdx].data[canonicalPath] += val
}

func (c *Cache) Has(path path.Path) bool {
	canonicalPath := c.symmetry.CanonicalizePath(path)
	shardIdx := c.getShardKey(canonicalPath)
	c.shards[shardIdx].RLock()
	defer c.shards[shardIdx].RUnlock()
	_, found := c.shards[shardIdx].data[canonicalPath]
	return found
}

func (c *Cache) Delete(path path.Path) {
	canonicalPath := c.symmetry.CanonicalizePath(path)
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
			if ctx.Err() != nil {
				return nil
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
