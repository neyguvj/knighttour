package searcher

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

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

func TestSearcherCaching(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	s := state.State(0).Visit(0)
	result1 := searcher.CountPaths(context.Background(), 0)

	s = state.State(0).Visit(0)
	pos := path.New(s, 0, 0)
	result2 := searcher.CountPathsDFS(context.Background(), pos)

	assert.Equal(t, result1.TotalPathsFound, result2.TotalPathsFound,
		"Cached and uncached results should match: %d vs %d", result1.TotalPathsFound, result2.TotalPathsFound)
}

func TestGenerateSubtasksDepthZero(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	st := state.State(0).Visit(0)
	p := path.New(st, 0, 0)

	ctx := context.Background()
	subtasks := searcher.GenerateSubtasks(ctx, p, 0)

	assert.Equal(t, 1, len(subtasks), "Depth 0 should return single subtask")
	assert.Equal(t, p.State(), subtasks[0].State(), "State should be preserved")
	assert.Equal(t, p.Start(), subtasks[0].Start(), "Start should be preserved")
	assert.Equal(t, p.End(), subtasks[0].End(), "End should be preserved")
}

func TestGenerateSubtasksDepthTwo(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	ctx := context.Background()

	st := state.State(0).Visit(0)
	p := path.New(st, 0, 0)

	subtasks := searcher.GenerateSubtasks(ctx, p, 2)

	assert.Greater(t, len(subtasks), 0, "Should generate subtasks for depth 2")

	for _, subtask := range subtasks {
		assert.True(t, subtask.State().IsVisited(0), "All subtasks should visit starting position")
		assert.NotEqual(t, subtask.Start(), subtask.End(), "Start and end should differ after depth 1")
	}
}

func TestGenerateSubtasksWithMetadataCanonicalPaths(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	ctx := context.Background()
	subtasks := searcher.GenerateSubtasksWithMetadata(ctx, 0, 4, 1)

	assert.Greater(t, len(subtasks), 0, "Should generate subtasks")

	for _, task := range subtasks {
		p := path.New(task.State, task.Start, task.End)
		canonical := sym.CanonicalizePath(p)

		assert.Equal(t, canonical.Start(), task.Start,
			"Subtask start should be canonical: got %d, want %d", task.Start, canonical.Start())
		assert.Equal(t, canonical.End(), task.End,
			"Subtask end should be canonical: got %d, want %d", task.End, canonical.End())
		assert.Equal(t, uint64(canonical.State()), uint64(task.State),
			"Subtask state should be canonical")
	}
}

func TestGenerateSubtasksWithMetadataOrbitMultiplier(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)

	ctx := context.Background()
	subtasks := searcher.GenerateSubtasksWithMetadata(ctx, 0, 4, 1)

	rawSubtasks := searcher.GenerateSubtasks(ctx, path.New(state.State(0).Visit(0), 0, 0), 1)

	totalMultiplier := 0
	for _, task := range subtasks {
		totalMultiplier += task.SymmetriesCount
	}

	canonMap := make(map[uint64]int)
	for _, rt := range rawSubtasks {
		canonical := sym.CanonicalizePath(rt)
		key := uint64(canonical.State())<<32 | uint64(canonical.Start())<<16 | uint64(canonical.End())
		canonMap[key]++
	}

	assert.Equal(t, len(canonMap), len(subtasks),
		"Number of unique canonical subtasks should match generated subtasks")

	expectedTotal := 0
	for _, count := range canonMap {
		expectedTotal += 4 * count
	}

	assert.Equal(t, expectedTotal, totalMultiplier,
		"Sum of OrbitIDs should equal sum of orbitSize*count for each canonical: got %d, want %d", totalMultiplier, expectedTotal)
}

func TestGenerateSubtasksWithMetadataAllCanonical(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	ctx := context.Background()

	for start := 0; start < 25; start++ {
		orbitSize := sym.GetOrbitSize(start)
		subtasks := searcher.GenerateSubtasksWithMetadata(ctx, start, orbitSize, 1)

		for _, task := range subtasks {
			p := path.New(task.State, task.Start, task.End)
			canonical := sym.CanonicalizePath(p)

			assert.Equal(t, canonical.Start(), task.Start,
				"Task from start=%d has non-canonical start: got %d, want %d", start, task.Start, canonical.Start())
			assert.Equal(t, canonical.End(), task.End,
				"Task from start=%d has non-canonical end: got %d, want %d", start, task.End, canonical.End())
		}
	}
}

func TestGenerateSubtasksWithMetadataMultipleDepths(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	ctx := context.Background()

	tests := []struct {
		name  string
		start int
		depth int
	}{
		{"depth 0 from corner", 0, 0},
		{"depth 1 from corner", 0, 1},
		{"depth 2 from corner", 0, 2},
		{"depth 1 from center", 12, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subtasks := searcher.GenerateSubtasksWithMetadata(ctx, tt.start, sym.GetOrbitSize(tt.start), tt.depth)

			if tt.depth == 0 {
				assert.Equal(t, 1, len(subtasks), "Depth 0 should return single subtask")
				return
			}

			for _, task := range subtasks {
				p := path.New(task.State, task.Start, task.End)
				canonical := sym.CanonicalizePath(p)

				assert.Equal(t, canonical.Start(), task.Start,
					"Subtask start should be canonical")
				assert.Equal(t, task.State.CountBits(), tt.depth,
					"Subtask state should have %d visited cells", tt.depth+1)
			}
		})
	}
}

func TestGenerateSubtasksWithMetadataConsistencyAcrossOrbits(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	ctx := context.Background()

	tests := []struct {
		pos     int
		wantLen int
	}{
		{0, 4},  // corner
		{1, 8},  // edge
		{6, 4},  // diagonal
		{12, 1}, // center
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("pos=%d", tt.pos), func(t *testing.T) {
			searcher := NewSearcher(g, sym)
			subtasks := searcher.GenerateSubtasksWithMetadata(ctx, tt.pos, tt.wantLen, 0)

			assert.Equal(t, 1, len(subtasks), "Depth 0 should return single subtask")
			assert.Equal(t, tt.wantLen, subtasks[0].SymmetriesCount,
				"OrbitID should equal orbit size for depth 0")
		})
	}
}

func TestGenerateSubtasksWithMetadataNoDuplicates(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	ctx := context.Background()

	subtasks := searcher.GenerateSubtasksWithMetadata(ctx, 0, 4, 2)

	stateMap := make(map[uint64]bool)
	for _, task := range subtasks {
		stateKey := uint64(task.State)<<32 | uint64(task.Start)<<16 | uint64(task.End)
		if stateMap[stateKey] {
			assert.Fail(t, "Duplicate subtask detected", "State=%v, Start=%d, End=%d", task.State, task.Start, task.End)
		}
		stateMap[stateKey] = true
	}
}

func TestGenerateSubtasksWithMetadataPathValidity(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	ctx := context.Background()

	subtasks := searcher.GenerateSubtasksWithMetadata(ctx, 0, 4, 2)

	for _, task := range subtasks {
		assert.GreaterOrEqual(t, task.Start, 0, "Start should be non-negative")
		assert.Less(t, task.Start, 25, "Start should be < 25")
		assert.GreaterOrEqual(t, task.End, 0, "End should be non-negative")
		assert.Less(t, task.End, 25, "End should be < 25")
		assert.GreaterOrEqual(t, task.Depth, 0, "Depth should be non-negative")

		bits := task.State.CountBits()
		assert.Equal(t, bits, task.Depth,
			"State should have depth+1 visited cells: got %d, want %d", bits, task.Depth+1)
	}
}

func TestGenerateSubtasksConnectivityPruning(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	ctx := context.Background()

	st := state.State(0).Visit(12)
	p := path.New(st, 12, 12)

	subtasks := searcher.GenerateSubtasks(ctx, p, 3)

	for _, subtask := range subtasks {
		assert.Equal(t, subtask.State().CountBits(), 3,
			"Each subtask at depth 3 should have 3 visited cells")
	}
}

func TestGenerateSubtasksWithMetadataSymmetryInvariance(t *testing.T) {
	g := graph.New(5)
	sym := symmetry.NewSymmetry(5)
	searcher := NewSearcher(g, sym)
	ctx := context.Background()

	p1 := searcher.GenerateSubtasksWithMetadata(ctx, 0, 4, 1)
	p2 := searcher.GenerateSubtasksWithMetadata(ctx, 4, 4, 1)
	p3 := searcher.GenerateSubtasksWithMetadata(ctx, 20, 4, 1)
	p4 := searcher.GenerateSubtasksWithMetadata(ctx, 24, 4, 1)

	assert.Equal(t, len(p1), len(p2),
		"Symmetric starts should produce same number of subtasks")
	assert.Equal(t, len(p1), len(p3),
		"Symmetric starts should produce same number of subtasks")
	assert.Equal(t, len(p1), len(p4),
		"Symmetric starts should produce same number of subtasks")

	canonicalP1 := sym.CanonicalizePath(path.New(p1[0].State, p1[0].Start, p1[0].End))
	canonicalP2 := sym.CanonicalizePath(path.New(p2[0].State, p2[0].Start, p2[0].End))

	assert.Equal(t, canonicalP1.Start(), canonicalP2.Start(),
		"Symmetric subtasks should have same canonical start")
	assert.Equal(t, canonicalP1.End(), canonicalP2.End(),
		"Symmetric subtasks should have same canonical end")

	stateSet := make(map[uint64]bool)
	for _, s := range p1 {
		key := uint64(s.State)
		stateSet[key] = true
	}

	assert.Equal(t, len(stateSet), len(p1),
		"All subtasks from one start should have unique states")
}
