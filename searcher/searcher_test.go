package searcher

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/oracle"
	"knighttour/path"
	"knighttour/state"
	"knighttour/symmetry"
	"knighttour/types"
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

	assert.Equal(t, 1, result.CacheWrites, "Should cache 1 path when depth=0")
}

func TestSearcherGenerateSubtasksPartialDepth(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	cache := cache.NewCache(sym)

	result := searcher.GenerateSubtasks(context.Background(), cache, 0, 1, 3)

	assert.GreaterOrEqual(t, result.CacheWrites, 1, "Should cache at least 1 path when depth=3")

	full := searcher.CountPaths(context.Background(), 0)
	assert.Positive(t, full.Pruned, "Pruner should cut branches during full counting")
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
		size        int
		depth       int
		oracleDepth int
	}{
		{size: 5, depth: 1, oracleDepth: 1},
		{size: 5, depth: 2, oracleDepth: 2},
		{size: 5, depth: 6, oracleDepth: 6},
		{size: 5, depth: 6, oracleDepth: 12}, // stop level above roots: plain DFS path
		{size: 5, depth: 1, oracleDepth: 16}, // deep oracle: single-lookup completions
		{size: 5, depth: 1, oracleDepth: 24}, // |U| = n²-1: deepest oracle level
	}

	for _, tt := range tests {
		t.Run("size"+strconv.Itoa(tt.size)+"/depth"+strconv.Itoa(tt.depth)+"/oracle"+strconv.Itoa(tt.oracleDepth), func(t *testing.T) {
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

			o := oracle.New(g)

			var fullTotal, revTotal int64
			prefixCache.Each(context.Background(), 1, func(_ context.Context, p path.Path, weight int) error {
				full := searcher.CountPathsDFS(context.Background(), p).TotalPathsFound
				withRev := searcher.CountPathsWithReversal(context.Background(), p, o, tt.oracleDepth).TotalPathsFound

				assert.Equal(t, full, withRev, "task %v: oracle early stop must match full descent", p)
				fullTotal += int64(full) * int64(weight)
				revTotal += int64(withRev) * int64(weight)
				return nil
			})

			assert.Positive(t, fullTotal)
			assert.Equal(t, fullTotal, revTotal)
		})
	}
}

func TestSearcherCacheReversalMatchesFullCount(t *testing.T) {
	tests := []struct {
		size  int
		depth int
	}{
		{size: 5, depth: 1},
		{size: 5, depth: 6},
		{size: 5, depth: 12}, // deepest reachable prefix-cache reversal
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
			prefixCache.Each(context.Background(), 1, func(_ context.Context, p path.Path, weight int) error {
				full := searcher.CountPathsDFS(context.Background(), p).TotalPathsFound
				withRev := searcher.CountPathsWithCacheReversal(context.Background(), p, prefixCache, tt.depth).TotalPathsFound

				assert.Equal(t, full, withRev, "task %v: cache early stop must match full descent", p)
				fullTotal += int64(full) * int64(weight)
				revTotal += int64(withRev) * int64(weight)
				return nil
			})

			assert.Positive(t, fullTotal)
			assert.Equal(t, fullTotal, revTotal)
		})
	}
}

func TestExtendSubtaskMatchesSinglePhase(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	ctx := context.Background()

	const baseDepth, targetDepth = 2, 4

	direct := cache.NewCache(sym)
	for _, group := range sym.GetCanonicalGroups() {
		searcher.GenerateSubtasks(ctx, direct, group.Canonical, group.OrbitSize, targetDepth)
	}

	intermediate := cache.NewCache(sym)
	for _, group := range sym.GetCanonicalGroups() {
		searcher.GenerateSubtasks(ctx, intermediate, group.Canonical, group.OrbitSize, baseDepth)
	}

	assert.Positive(t, intermediate.ItemsCount(), "Intermediate cache must not be empty")

	extended := cache.NewCache(sym)
	for _, e := range intermediate.Entries() {
		result := searcher.ExtendSubtask(ctx, extended, e.Path, e.Weight, targetDepth)
		assert.Positive(t, result.CacheWrites, "Extension of an entry generates leaves at target depth")
	}

	assert.Equal(t, direct.ItemsCount(), extended.ItemsCount(), "Two-phase cache has the same key set")
	for _, e := range direct.Entries() {
		weight, ok := extended.GetCanonical(e.Path)
		assert.True(t, ok, "Key %v present in two-phase cache", e.Path)
		assert.Equal(t, e.Weight, weight, "Weight of key %v matches single phase", e.Path)
	}
}

func TestExtendSubtaskNoopBeyondDepth(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	st := state.State(0).Visit(0).Visit(6)
	p := path.New(st, 6)

	c := cache.NewCache(sym)
	result := searcher.ExtendSubtask(context.Background(), c, p, 1, 2)

	assert.Zero(t, result.CacheWrites, "Entry already at target depth generates nothing")
	assert.Equal(t, 0, c.ItemsCount())
}

func TestSearcherResultStatistics(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	prefixCache := cache.NewCache(sym)
	var genTotal types.Result
	for _, group := range sym.GetCanonicalGroups() {
		result := searcher.GenerateSubtasks(context.Background(), prefixCache, group.Canonical, group.OrbitSize, 3)
		assert.Equal(t, result.PrunedDeadEnd+result.PrunedNoCont+result.PrunedDisconn+result.PrunedEndpoints, result.Pruned,
			"pruned breakdown must sum to the total")
		genTotal.Add(result)
	}

	assert.Positive(t, genTotal.CacheWrites, "generation writes cache entries")
	assert.Zero(t, genTotal.CacheHits+genTotal.CacheMisses, "generation performs no reversal lookups")
	assert.Positive(t, genTotal.Pruned, "pruning fires during prefix generation")
	assert.Positive(t, genTotal.PrunedDisconn+genTotal.PrunedEndpoints, "global checks account for early-depth pruning")

	var countTotal types.Result
	prefixCache.Each(context.Background(), 1, func(_ context.Context, p path.Path, _ int) error {
		countTotal.Add(searcher.CountPathsWithCacheReversal(context.Background(), p, prefixCache, 3))
		return nil
	})

	assert.Positive(t, countTotal.CacheHits+countTotal.CacheMisses, "cache reversal performs lookups")
	assert.Zero(t, countTotal.CacheWrites, "counting writes nothing")
	assert.Equal(t, countTotal.PrunedDeadEnd+countTotal.PrunedNoCont+countTotal.PrunedDisconn+countTotal.PrunedEndpoints, countTotal.Pruned)
}
