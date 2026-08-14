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
	startPos := 0

	pos := path.New(s, startPos, 0)

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
	pos := path.New(s, 0, 0)
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
	pos := path.New(s, 0, posIdx)

	cache.Set(pos, 123)

	count, ok := cache.Get(pos)
	assert.True(t, ok, "Cache should contain value for high bit state")
	assert.Equal(t, 123, count, "Expected 123")

	s2 := state.State(1 << 40)
	pos2Idx := sym.GetCanonicalPosition(63)
	pos2 := path.New(s2, 0, pos2Idx)

	count2, ok2 := cache.Get(pos2)
	assert.False(t, ok2, "Different state with high bit should not return value")
	assert.Equal(t, 0, count2, "Expected 0 for different state")
}
