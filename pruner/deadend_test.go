package pruner

import (
	"testing"

	"knighttour/graph"
	"knighttour/path"
	"knighttour/state"

	"github.com/stretchr/testify/assert"
)

func TestDeadEndPrune_EmptyState(t *testing.T) {
	g := graph.New(5)
	st := state.State(0)
	pruner := NewDeadEndPruner(g)

	result := pruner.ShouldPrune(path.New(st, 0, 0))
	assert.False(t, result, "Empty path should not be pruned")
}

func TestDeadEndPrune_FullState(t *testing.T) {
	g := graph.New(5)
	st := state.State((1 << 25) - 1)
	pruner := NewDeadEndPruner(g)

	result := pruner.ShouldPrune(path.New(st, 0, 24))
	assert.False(t, result, "Full path should not be pruned")
}

func TestDeadEndPrune_SingleUnvisited(t *testing.T) {
	g := graph.New(5)
	st := state.State(0)
	for i := 0; i < 24; i++ {
		st = st.Visit(i)
	}
	pruner := NewDeadEndPruner(g)

	result := pruner.ShouldPrune(path.New(st, 0, 23))
	assert.True(t, result, "Single unvisited vertex should be pruned (no path to it)")
}

func TestDeadEndPrune_IsolatedVertexCorner(t *testing.T) {
	g := graph.New(5)
	st := state.State(0).Visit(1).Visit(3).Visit(5).Visit(7).Visit(8).Visit(9).Visit(10).Visit(11).Visit(12).Visit(13).Visit(14).Visit(15).Visit(16).Visit(17).Visit(18).Visit(19).Visit(20).Visit(21).Visit(22).Visit(23).Visit(24)
	pruner := NewDeadEndPruner(g)

	result := pruner.ShouldPrune(path.New(st, 0, 24))
	assert.True(t, result, "Isolated vertex at corner should be pruned")
}

func TestDeadEndPrune_IsolatedVertexCenter(t *testing.T) {
	g := graph.New(5)
	st := state.State(0)
	for i := 0; i < 25; i++ {
		if i != 12 {
			st = st.Visit(i)
		}
	}
	pruner := NewDeadEndPruner(g)

	result := pruner.ShouldPrune(path.New(st, 0, 24))
	assert.True(t, result, "Isolated vertex at center should be pruned")
}

func TestDeadEndPrune_TwoIsolatedVertices(t *testing.T) {
	g := graph.New(5)
	st := state.State(0).Visit(0).Visit(1).Visit(2).Visit(3).Visit(4).Visit(5).Visit(6).Visit(7).Visit(8).Visit(9).Visit(10).Visit(11).Visit(13).Visit(14).Visit(15).Visit(16).Visit(17).Visit(18).Visit(19).Visit(20).Visit(21).Visit(22).Visit(23).Visit(24)
	pruner := NewDeadEndPruner(g)

	result := pruner.ShouldPrune(path.New(st, 0, 24))
	assert.True(t, result, "Two isolated vertices should be pruned")
}

func TestDeadEndPrune_PathOnLine(t *testing.T) {
	g := graph.New(5)
	st := state.State(0).Visit(0).Visit(1)
	pruner := NewDeadEndPruner(g)

	result := pruner.ShouldPrune(path.New(st, 0, 1))
	assert.False(t, result, "Valid path should not be pruned")
}

func TestDeadEndPrune_LargeBoard(t *testing.T) {
	g := graph.New(5)
	st := state.State(0).Visit(0)
	for i := 1; i < 25; i++ {
		if i != 12 {
			st = st.Visit(i)
		}
	}
	pruner := NewDeadEndPruner(g)

	result := pruner.ShouldPrune(path.New(st, 0, 24))
	assert.True(t, result, "Isolated vertex should be pruned on 5x5")
}
