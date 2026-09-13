package cache

import (
	"sync"

	"knighttour/path"
)

// numShards is the shard count of both tables (Accumulator and Cache); it
// must stay 1<<7 to match the shift in shardIndex.
const numShards = 128

// accShard is one Accumulator shard: its map plus the exclusive lock guarding it.
type accShard struct {
	data map[path.Path]uint64
	mu   sync.Mutex
}

// Accumulator is the intermediate gen-A weight table of the pipeline
// (specs/cache.md): one of the two additive "key → Σ weights" tables (the
// task-cache is Cache). Keys are D4-canonical placements (state, end), weights
// sum orbit sizes. Entries exist only for keys actually added — zeros are
// never stored. Thread-safe via sharding; read through Drain after the writing
// phase ends (the copying Snapshot was removed: it doubled peak memory on
// large boards).
type Accumulator struct {
	shards [numShards]accShard
}

func NewAccumulator() *Accumulator {
	a := &Accumulator{}
	for i := range a.shards {
		a.shards[i].data = make(map[path.Path]uint64)
	}
	return a
}

// shardIndex is the shard hash shared by both tables (Accumulator and
// Cache). It hashes State only, so every end of one mask lands in the same
// shard — the key's ends are never split across shards (specs/cache.md,
// ADR-004); for the task-cache it merely spreads contention.
// Allocation-free: golden-ratio multiply, take the high bits. numShards must
// stay 1<<7 to match the shift.
func shardIndex(p path.Path) int {
	h := uint64(p.State()) * 0x9E3779B97F4A7C15
	return int(h >> (64 - 7)) // numShards = 128
}

// Add accumulates one contribution: data[key] += weight.
func (a *Accumulator) Add(p path.Path, weight uint64) {
	sh := &a.shards[shardIndex(p)]
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

// drainShard hands out shard i's records and releases its map, so peak memory
// of the drain stays bounded by one shard plus the result (specs/cache.md).
// Internal mechanics of Drain — public reads go through Drain only. Call after
// all writers stopped; further Add on a drained accumulator panics (nil map) —
// the pipeline never does. The shard is locked only for its own copy pass.
func (a *Accumulator) drainShard(i int) []Entry {
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

// Drain concatenates every shard and empties the table (the gen-A worklist).
// Each record is handed out exactly once; a shard's map is released as soon as
// its entries are taken.
func (a *Accumulator) Drain() []Entry {
	out := make([]Entry, 0, a.ItemsCount())
	for i := range a.shards {
		out = append(out, a.drainShard(i)...)
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
