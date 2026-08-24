package searcher

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/path"
	"knighttour/state"
	"knighttour/symmetry"
)

func TestSearcherCountPaths(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	total := 0
	for start := 0; start < 25; start++ {
		result := searcher.CountPaths(context.Background(), start)
		total += result.TotalPathsFound
	}

	assert.Equal(t, 1728, total, "Expected 1728 for 5x5 board")
}

func TestSearcherCountFromState(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	s := state.State(0).Visit(0)
	pos := path.New(s, 0, 0)

	result := searcher.CountPathsDFS(context.Background(), pos)

	assert.NotEqual(t, 0, result.TotalPathsFound, "Should find paths from valid starting position")
}

func TestSearcherGenerateSubtasksDepthZero(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	cache := cache.NewCache(sym)

	result := searcher.GenerateSubtasks(context.Background(), cache, 0, 1, 0)

	assert.Equal(t, 1, result.CachedPaths, "Should cache 1 path when depth=0")
}

func TestSearcherGenerateSubtasksPartialDepth(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	cache := cache.NewCache(sym)

	result := searcher.GenerateSubtasks(context.Background(), cache, 0, 1, 3)

	assert.GreaterOrEqual(t, result.CachedPaths, 1, "Should cache at least 1 path when depth=3")
}

func TestSearcherCountPathsDFSFromPartialPath(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	s := state.State(0).Visit(0).Visit(8)
	pos := path.New(s, 0, 8)

	result := searcher.CountPathsDFS(context.Background(), pos)

	assert.NotEqual(t, 0, result.TotalPathsFound, "Should find paths from partial path")
}
