package cache

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/path"
	"knighttour/state"
)

func TestAccumulatorAddAndDrain(t *testing.T) {
	acc := NewAccumulator()
	k1 := path.New(state.State(0b101), 1)
	k2 := path.New(state.State(0b101), 2)

	acc.Add(k1, 3)
	acc.Add(k1, 4)
	acc.Add(k2, 5)

	assert.Equal(t, 2, acc.ItemsCount())
	drained := acc.Drain()
	sums := make(map[path.Path]uint64, len(drained))
	for _, e := range drained {
		sums[e.Path] += e.Weight
	}
	assert.Equal(t, uint64(7), sums[k1])
	assert.Equal(t, uint64(5), sums[k2])
	assert.Zero(t, acc.ItemsCount(), "drain must empty the table")
}

// Drain returns the full table exactly once: every record appears with its
// summed weight and a second drain (or ItemsCount) sees nothing (specs/cache.md).
func TestAccumulatorDrainReturnsTableOnce(t *testing.T) {
	acc := NewAccumulator()
	const n = 3000
	for i := range n {
		acc.Add(path.New(state.State(i), i%17), uint64(i))
	}

	drained := acc.Drain()
	assert.Len(t, drained, n, "Drain returns every record once")
	var sum uint64
	for _, e := range drained {
		sum += e.Weight
	}
	var want uint64
	for i := range n {
		want += uint64(i)
	}
	assert.Equal(t, want, sum)

	assert.Zero(t, acc.ItemsCount(), "drained table is empty")
	assert.Empty(t, acc.Drain(), "a second drain returns nothing")
}

// Sharding invariant (specs/cache.md): every end of one State lives in the
// same shard — internal drainShard mechanics must never split a mask.
func TestSameStateSharesOneShard(t *testing.T) {
	acc := NewAccumulator()
	states := []state.State{0b1, 0b1001, 0xFF00FF, 0x123456789ABCDEF}
	const ends = 8
	for _, st := range states {
		for e := range ends {
			acc.Add(path.New(st, e), 1)
		}
	}

	shardOf := make(map[state.State]map[int]bool, len(states))
	for i := range acc.shards {
		for _, e := range acc.drainShard(i) {
			st := e.Path.State()
			if shardOf[st] == nil {
				shardOf[st] = make(map[int]bool)
			}
			shardOf[st][i] = true
		}
	}
	for _, st := range states {
		assert.Len(t, shardOf[st], 1, "state %b spread across shards", st)
	}
}

// LocalSink must reproduce direct Add semantics: duplicates collapse locally,
// threshold flushes and explicit Flush conserve the total per key.
func TestLocalSinkMatchesDirectAdd(t *testing.T) {
	const n = 5000 // > localFlushLimit several times over
	direct := NewAccumulator()
	buffered := NewAccumulator()

	sink := buffered.Local()
	for i := range n {
		k := path.New(state.State(i%700), i%13)
		w := uint64(i%5 + 1)
		direct.Add(k, w)
		sink.Add(k, w)
	}
	sink.Flush()

	assert.Equal(t, direct.ItemsCount(), buffered.ItemsCount())
	want := make(map[path.Path]uint64)
	for _, e := range direct.Drain() {
		want[e.Path] = e.Weight
	}
	for _, e := range buffered.Drain() {
		assert.Equal(t, want[e.Path], e.Weight, "key %v", e.Path)
		delete(want, e.Path)
	}
	assert.Empty(t, want)
}

// Concurrent sinks (one per goroutine) merged into one accumulator must be
// race-free under -race and conserve the total weight.
func TestLocalSinkConcurrent(t *testing.T) {
	const writers = 8
	const perWriter = 4000

	acc := NewAccumulator()
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			sink := acc.Local()
			for i := range perWriter {
				sink.Add(path.New(state.State(i), w), 1)
			}
			sink.Flush()
		})
	}
	wg.Wait()

	var total uint64
	for _, e := range acc.Drain() {
		total += e.Weight
	}
	assert.Equal(t, uint64(writers*perWriter), total)
}
