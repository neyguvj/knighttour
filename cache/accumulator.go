package cache

import (
	"sync"

	"knighttour/path"
)

const numAccShards = 128

type accShard struct {
	data map[path.Path]uint64
	mu   sync.Mutex
}

// Accumulator is the single weight table of the pipeline (specs/cache.md):
// an additive multiset "key → Σ weights" shared by both generation phases.
// Phase A keys are D4-canonical placements (state, end); phase B keys are
// translation+D4 normalized complement shape classes (specs/shapecount.md).
// Entries exist only for keys actually added — zeros are never stored.
// Thread-safe via sharding; read through DrainShard/Drain after the writing
// phase ends (the copying Snapshot was removed: it doubled peak memory on
// large boards).
type Accumulator struct {
	shards [numAccShards]accShard
}

func NewAccumulator() *Accumulator {
	a := &Accumulator{}
	for i := range a.shards {
		a.shards[i].data = make(map[path.Path]uint64)
	}
	return a
}

// accShardIndex hashes State only, so every end of one shape class lands in
// the same shard and downstream can group by shape per shard without a global
// sort (specs/cache.md).
func accShardIndex(p path.Path) int {
	h := uint64(p.State()) * 0x9E3779B97F4A7C15
	return int(h >> (64 - 7)) // numAccShards = 128
}

// Add accumulates one contribution: data[key] += weight.
func (a *Accumulator) Add(p path.Path, weight uint64) {
	sh := &a.shards[accShardIndex(p)]
	sh.mu.Lock()
	sh.data[p] += weight
	sh.mu.Unlock()
}

// localFlushLimit bounds a LocalSink buffer; 1024 keys collapse the hot
// duplicates of thousands of leaves before any lock is taken (specs/cache.md).
const localFlushLimit = 1024

// LocalSink buffers Add on the owner side and merges into the shared
// Accumulator in per-shard batches. Buffers are not merged across sinks; the
// table is additive, so flush timing never changes results. Not safe for
// sharing between goroutines — one sink per worker/task.
type LocalSink struct {
	parent *Accumulator
	buf    map[path.Path]uint64
}

// Local returns a fresh buffered writer into a.
func (a *Accumulator) Local() *LocalSink {
	return &LocalSink{parent: a, buf: make(map[path.Path]uint64, 128)}
}

func (s *LocalSink) Add(p path.Path, weight uint64) {
	s.buf[p] += weight
	if len(s.buf) >= localFlushLimit {
		s.Flush()
	}
}

// Flush merges the buffer into the shared accumulator. Duplicates are already
// collapsed locally, so each distinct key costs one shard lock per flush.
func (s *LocalSink) Flush() {
	if len(s.buf) == 0 {
		return
	}
	for k, w := range s.buf {
		s.parent.Add(k, w)
	}
	clear(s.buf)
}

// Entry is one accumulator record.
type Entry struct {
	Path   path.Path
	Weight uint64
}

// NumShards returns the number of independently drainable shards.
func (a *Accumulator) NumShards() int { return numAccShards }

// DrainShard hands out shard i's records and releases its map, so peak memory
// of a per-shard consumer stays bounded by one shard (specs/cache.md). Call
// only after all writers stopped; further Add on a drained accumulator panics
// (nil map) — the pipeline never does. The shard is locked only for its own
// copy pass.
func (a *Accumulator) DrainShard(i int) []Entry {
	sh := &a.shards[i]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	out := make([]Entry, 0, len(sh.data))
	for k, w := range sh.data {
		out = append(out, Entry{Path: k, Weight: w})
	}
	sh.data = nil
	return out
}

// Drain concatenates every shard and empties the table. Meant for the small
// gen A worklist; large M tables are consumed per-shard via DrainShard.
func (a *Accumulator) Drain() []Entry {
	out := make([]Entry, 0, a.ItemsCount())
	for i := range a.shards {
		out = append(out, a.DrainShard(i)...)
	}
	return out
}

func (a *Accumulator) ItemsCount() int {
	total := 0
	for i := range a.shards {
		sh := &a.shards[i]
		sh.mu.Lock()
		total += len(sh.data)
		sh.mu.Unlock()
	}
	return total
}
