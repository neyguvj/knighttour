package counter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/path"
)

func TestSequentalCount(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 1)

	assert.Equal(t, uint64(1728), count, "Expected %d count for 5x5 board, got %d", 1728, count)
}

func TestParallelCount(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.ParallelCount(context.Background(), monitoring.NewFakeMonitor(), 8)

	assert.Equal(t, uint64(1728), count, "Expected %d count for 5x5 board, got %d", 1728, count)
}

func TestParallelCountWithDepth(t *testing.T) {
	size := 5
	g := graph.New(size)
	counter := NewCounter(g)

	for depth := range size * size {
		count := counter.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), 8, depth)
		assert.Equal(t, uint64(1728), count, "Expected %d count for 5x5 board, got %d", 1728, count)
	}
}

func TestParallelCountWithDepthMatchesReference(t *testing.T) {
	tests := []struct {
		name     string
		depths   []int
		size     int
		expected uint64
	}{
		{name: "5x5 depths 1-4", size: 5, expected: 1728, depths: []int{1, 2, 3, 4}},
		{name: "6x6 depths 1-10", size: 6, expected: 6_637_920, depths: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New(tt.size)
			counter := NewCounter(g)

			for _, depth := range tt.depths {
				count := counter.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), 4, depth)
				assert.Equal(t, tt.expected, count, "size=%d depth=%d", tt.size, depth)
			}
		})
	}
}

func TestGenerateSubTasksWeightsMatchOrbits(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	// Per group: every raw prefix contributes its orbit size to the cache,
	// so total weight must equal CachedPaths * OrbitSize regardless of merging.
	for _, group := range counter.symmetry.GetCanonicalGroups() {
		if g.SholdSkip(group.Canonical) {
			continue
		}

		groupCache := cache.NewCache(counter.symmetry)
		result := counter.searcher.GenerateSubtasks(context.Background(), groupCache, group.Canonical, group.OrbitSize, 2)

		totalWeight := 0
		groupCache.Each(context.Background(), 1, func(_ context.Context, _ path.Path, weight int) {
			assert.Positive(t, weight, "weight must be positive")
			totalWeight += weight
		})

		assert.Equal(t, result.CachedPaths*group.OrbitSize, totalWeight,
			"group %d: total weight equals prefixes * orbit size", group.Canonical)
	}

	// Merging across groups conserves the total weight and never increases entry count.
	fullCache := counter.generateSubTasks(context.Background(), monitoring.NewFakeMonitor(), 4, 2)
	fullWeight := 0
	fullCache.Each(context.Background(), 1, func(_ context.Context, _ path.Path, weight int) {
		fullWeight += weight
	})

	expectedWeight := 0
	for _, group := range counter.symmetry.GetCanonicalGroups() {
		if g.SholdSkip(group.Canonical) {
			continue
		}
		groupCache := cache.NewCache(counter.symmetry)
		result := counter.searcher.GenerateSubtasks(context.Background(), groupCache, group.Canonical, group.OrbitSize, 2)
		expectedWeight += result.CachedPaths * group.OrbitSize
	}

	assert.Equal(t, expectedWeight, fullWeight, "cross-group merging conserves total weight")
}

func TestCounterFromPosition(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	count := counter.CountFromPosition(context.Background(), 0)

	assert.Positive(t, count, "Should find paths from valid starting position")
}

func TestTwoPhaseMatchesSinglePhase(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)
	ctx := context.Background()

	const depth = 7 // > TwoPhaseBaseDepth: generateSubTasks must go two-phase

	direct := cache.NewCache(counter.symmetry)
	for _, group := range counter.symmetry.GetCanonicalGroups() {
		counter.searcher.GenerateSubtasks(ctx, direct, group.Canonical, group.OrbitSize, depth)
	}

	twoPhase := counter.generateSubTasks(ctx, monitoring.NewFakeMonitor(), 8, depth)

	assert.Equal(t, direct.ItemsCount(), twoPhase.ItemsCount(), "Two-phase cache has the same key set")
	for _, e := range direct.Entries() {
		weight, ok := twoPhase.GetCanonical(e.Path)
		assert.True(t, ok, "Key %v present in two-phase cache", e.Path)
		assert.Equal(t, e.Weight, weight, "Weight of key %v matches single phase", e.Path)
	}
}

func TestTwoPhaseParallelMatchesSequential(t *testing.T) {
	g := graph.New(5)
	counter := NewCounter(g)

	countSeq := counter.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), 1, 6)
	countPar := counter.ParallelCountWithDepth(context.Background(), monitoring.NewFakeMonitor(), 8, 7)

	assert.Equal(t, uint64(1728), countSeq)
	assert.Equal(t, countSeq, countPar, "Two-phase results must not depend on worker count or depth")
}
