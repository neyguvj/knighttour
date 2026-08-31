package searcher

import (
	"context"
	"strconv"
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
	for start := range 25 {
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
	pos := path.New(s, 0)

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
	pos := path.New(s, 8)

	result := searcher.CountPathsDFS(context.Background(), pos)

	assert.NotEqual(t, 0, result.TotalPathsFound, "Should find paths from partial path")
}

func TestSearcherReversalMatchesFullCount(t *testing.T) {
	tests := []struct {
		size  int
		depth int
	}{
		{size: 5, depth: 1},
		{size: 5, depth: 2},
		{size: 5, depth: 6},
		{size: 5, depth: 12}, // 2d == n²-1: deepest reachable reversal level
		{size: 5, depth: 13}, // 2d > n²: guard disables early stop
	}

	for _, tt := range tests {
		t.Run("size"+strconv.Itoa(tt.size)+"/depth"+strconv.Itoa(tt.depth), func(t *testing.T) {
			g := graph.New(tt.size)
			sym := symmetry.NewSymmetry(tt.size)
			searcher := NewSearcher(g, sym)

			prefixCache := cache.NewCache(sym)
			for _, group := range sym.GetCanonicalGroups() {
				if g.SholdSkip(group.Canonical) {
					continue
				}
				searcher.GenerateSubtasks(context.Background(), prefixCache, group.Canonical, group.OrbitSize, tt.depth)
			}

			var fullTotal, revTotal int64
			prefixCache.Each(context.Background(), 1, func(_ context.Context, p path.Path, weight int) {
				full := searcher.CountPathsDFS(context.Background(), p).TotalPathsFound
				withRev := searcher.CountPathsWithReversal(context.Background(), p, prefixCache, tt.depth).TotalPathsFound

				assert.Equal(t, full, withRev, "task %v: early stop must match full descent", p)
				fullTotal += int64(full) * int64(weight)
				revTotal += int64(withRev) * int64(weight)
			})

			assert.Positive(t, fullTotal)
			assert.Equal(t, fullTotal, revTotal)
		})
	}
}
