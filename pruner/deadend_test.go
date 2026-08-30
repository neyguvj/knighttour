package pruner

import (
	"math/bits"
	"math/rand"
	"testing"

	"knighttour/graph"
	"knighttour/state"

	"github.com/stretchr/testify/assert"
)

func TestShouldPruneAfterVisit_NoUnvisited(t *testing.T) {
	g := graph.New(5)
	p := NewDeadEndPruner(g)

	result := p.ShouldPruneAfterVisit(0, state.State(0))
	assert.False(t, result, "No unvisited cells should not be pruned")
}

func TestShouldPruneAfterVisit_SingleUnvisitedReachable(t *testing.T) {
	g := graph.New(5)
	p := NewDeadEndPruner(g)

	result := p.ShouldPruneAfterVisit(24, state.Bit(13))
	assert.False(t, result, "Single reachable unvisited cell should not be pruned")
}

func TestShouldPruneAfterVisit_SingleUnvisitedUnreachable(t *testing.T) {
	g := graph.New(5)
	p := NewDeadEndPruner(g)

	result := p.ShouldPruneAfterVisit(23, state.Bit(24))
	assert.True(t, result, "Single unreachable unvisited cell should be pruned")
}

func TestShouldPruneAfterVisit_IsolatedVertexCorner(t *testing.T) {
	g := graph.New(5)
	p := NewDeadEndPruner(g)

	// Непосещённые 0, 2, 4, 6; клетка 0 (угол) изолирована и соседняя к last=7
	result := p.ShouldPruneAfterVisit(7, state.NewState(0, 2, 4, 6))
	assert.True(t, result, "Isolated vertex at corner should be pruned")
}

func TestShouldPruneAfterVisit_IsolatedVertexCenter(t *testing.T) {
	g := graph.New(5)
	p := NewDeadEndPruner(g)

	// Осталась только клетка 12 (центр), она не соседняя к last=24
	result := p.ShouldPruneAfterVisit(24, state.Bit(12))
	assert.True(t, result, "Isolated vertex at center should be pruned")
}

func TestShouldPruneAfterVisit_TwoIsolatedVertices(t *testing.T) {
	g := graph.New(5)
	p := NewDeadEndPruner(g)

	// Клетки 0 и 24 изолированы друг от друга; 0 соседняя к last=7
	result := p.ShouldPruneAfterVisit(7, state.NewState(0, 24))
	assert.True(t, result, "Two isolated vertices should be pruned")
}

func TestShouldPruneAfterVisit_ValidPathNotPruned(t *testing.T) {
	g := graph.New(5)
	p := NewDeadEndPruner(g)
	totalCells := g.GetTotalCells()
	fullMask := state.State(uint64(1)<<uint(totalCells) - 1)

	result := p.ShouldPruneAfterVisit(1, fullMask.AndNot(state.NewState(0, 1)))
	assert.False(t, result, "Valid path should not be pruned")
}

// fullScanShouldPrune — справочная реализация полного скана всех непосещённых клеток.
func fullScanShouldPrune(g *graph.Graph, last int, unvisited state.State) bool {
	if unvisited.IsEmpty() {
		return false
	}

	if unvisited.CountBits() == 1 {
		lone := int(unvisited.TrailingZeroBits())
		return g.GetNeighborMask(lone).Intersect(state.Bit(last)).IsEmpty()
	}

	for u := range unvisited.AllVisited() {
		if g.GetNeighborMask(u).Intersect(unvisited).IsEmpty() {
			return true
		}
	}

	return false
}

func TestShouldPruneAfterVisit_MatchesFullScan(t *testing.T) {
	g := graph.New(5)
	p := NewDeadEndPruner(g)
	totalCells := g.GetTotalCells()
	fullMask := state.State(uint64(1)<<uint(totalCells) - 1)

	rng := rand.New(rand.NewSource(42))

	for trial := 0; trial < 2000; trial++ {
		st := state.State(0).Visit(rng.Intn(totalCells))
		end := bits.TrailingZeros64(uint64(st))
		for step := 0; step < totalCells-1; step++ {
			unvisited := fullMask.AndNot(st)
			nbrs := g.GetNeighborMask(end).Intersect(unvisited)
			if nbrs.IsEmpty() {
				break
			}
			n := bits.TrailingZeros64(uint64(nbrs))
			bit := state.State(uint64(1) << uint(n))
			newSt := st | bit
			childUnvisited := unvisited.AndNot(bit)

			if fullScanShouldPrune(g, end, unvisited) {
				break // инвариант нарушен ниже не гарантируем эквивалентность
			}
			want := fullScanShouldPrune(g, int(n), childUnvisited)
			got := p.ShouldPruneAfterVisit(int(n), childUnvisited)
			assert.Equal(t, want, got, "trial %d step %d: local check != full scan", trial, step)

			st = newSt
			end = int(n)
		}
	}
}
