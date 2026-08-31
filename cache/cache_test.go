package cache

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/path"
	"knighttour/state"
	"knighttour/symmetry"
)

func TestCacheBasic(t *testing.T) {
	sym := symmetry.NewSymmetry(5)
	cache := NewCache(sym)

	s := state.State(0b101)

	pos := path.New(s, 0)

	count, ok := cache.Get(pos)
	assert.False(t, ok, "Empty cache should not return value")
	assert.Equal(t, 0, count, "Expected 0")

	cache.Set(pos, 42)

	count, ok = cache.Get(pos)
	assert.True(t, ok, "Cache should contain value after Set")
	assert.Equal(t, 42, count, "Expected 42")
}

func TestCacheClear(t *testing.T) {
	sym := symmetry.NewSymmetry(5)
	cache := NewCache(sym)

	s := state.State(0b101)
	pos := path.New(s, 0)
	cache.Set(pos, 42)

	count, ok := cache.Get(pos)
	assert.True(t, ok, "Cache should contain value before Clear")
	assert.Equal(t, 42, count, "Expected 42")

	cache.Clear()

	count, ok = cache.Get(pos)
	assert.False(t, ok, "Cache should be empty after Clear")
	assert.Equal(t, 0, count, "Expected 0")
}

func TestCacheHighBitState(t *testing.T) {
	sym := symmetry.NewSymmetry(8)
	cache := NewCache(sym)

	s := state.State((1 << 40) | (1 << 32))
	posIdx := sym.GetCanonicalPosition(63)
	pos := path.New(s, posIdx)

	cache.Set(pos, 123)

	count, ok := cache.Get(pos)
	assert.True(t, ok, "Cache should contain value for high bit state")
	assert.Equal(t, 123, count, "Expected 123")

	s2 := state.State(1 << 40)
	pos2Idx := sym.GetCanonicalPosition(63)
	pos2 := path.New(s2, pos2Idx)

	count2, ok2 := cache.Get(pos2)
	assert.False(t, ok2, "Different state with high bit should not return value")
	assert.Equal(t, 0, count2, "Expected 0 for different state")
}

func TestCacheWeightMergeAcrossOrbits(t *testing.T) {
	sym := symmetry.NewSymmetry(5)
	c := NewCache(sym)

	// On a 5x5 board cells 0 and 4 are in the same orbit (rotate 90),
	// so both single-cell tasks must merge into one key with summed weight.
	p1 := path.New(state.State(1<<0), 0)
	p2 := path.New(state.State(1<<4), 4)

	c.Set(p1, 3)
	c.Set(p2, 5)

	assert.Equal(t, 1, c.ItemsCount(), "Symmetric tasks should merge into a single entry")

	weight, ok := c.Get(p1)
	assert.True(t, ok)
	assert.Equal(t, 8, weight, "Weights of merged prefixes must sum up")

	weight, ok = c.Get(p2)
	assert.True(t, ok)
	assert.Equal(t, 8, weight, "Lookup by any symmetric key returns the same weight")
}

func TestCacheEntries(t *testing.T) {
	sym := symmetry.NewSymmetry(5)
	c := NewCache(sym)

	p1 := path.New(state.State(1<<0|1<<6), 6)
	p2 := path.New(state.State(1<<2|1<<8), 8)
	c.Set(p1, 7)
	c.Set(p2, 5)

	entries := c.Entries()
	assert.Len(t, entries, 2, "Snapshot contains every record")

	totalWeight := 0
	for _, e := range entries {
		assert.Positive(t, e.Weight)
		totalWeight += e.Weight
	}
	assert.Equal(t, 12, totalWeight, "Snapshot conserves total weight")

	c.Clear()
	assert.Empty(t, c.Entries(), "Cleared cache has no records")
}
