package pruner

import (
	"math/rand"
	"testing"

	"knighttour/graph"
	"knighttour/state"

	"github.com/stretchr/testify/assert"
)

func TestAdvanced_NoUnvisited(t *testing.T) {
	g := graph.New(8)
	p := NewAdvancedPruner(g)

	assert.False(t, p.ShouldPruneAfterVisit(0, state.State(0)))
}

// U = {0,17} (связная пара) + {7,22} (отдельная компонента); last=10 сосед к 0.
func TestAdvanced_DisconnectedRemainder(t *testing.T) {
	g := graph.New(8)
	p := NewAdvancedPruner(g)

	u := state.NewState(0, 17, 7, 22)
	assert.True(t, p.ShouldPruneAfterVisit(10, u), "disconnected remainder should be pruned")
}

// Цепочка 0-17-34, last=10 сосед к 0: связно, два конца, один достижим.
func TestAdvanced_ConnectedNotPruned(t *testing.T) {
	g := graph.New(8)
	p := NewAdvancedPruner(g)

	u := state.NewState(0, 17, 34)
	assert.False(t, p.ShouldPruneAfterVisit(10, u), "connected remainder with reachable endpoint should not be pruned")
}

// Звезда: центр 17 и три листа {0,2,34}; 3 вершины степени 1 -> prune.
func TestAdvanced_ThreeDegreeOneVertices(t *testing.T) {
	g := graph.New(8)
	p := NewAdvancedPruner(g)

	u := state.NewState(0, 2, 34, 17)
	assert.True(t, p.ShouldPruneAfterVisit(27, u), "three degree-1 vertices should be pruned")
}

// Цепочка 0-17-34, last=27 сосед только центру: ни один форсированный конец
// не достижим от last -> prune.
func TestAdvanced_ForcedEndpointsUnreachable(t *testing.T) {
	g := graph.New(8)
	p := NewAdvancedPruner(g)

	u := state.NewState(0, 17, 34)
	assert.True(t, p.ShouldPruneAfterVisit(27, u), "unreachable forced endpoints should be pruned")
}

// Нет соседа среди непосещённых — продолжения не существует.
func TestAdvanced_NoUnvisitedNeighbor(t *testing.T) {
	g := graph.New(8)
	p := NewAdvancedPruner(g)

	// last=0, клетки 7 и 22 не соседни 0 (но связны между собой).
	u := state.NewState(7, 22)
	assert.True(t, p.ShouldPruneAfterVisit(0, u), "no continuation should be pruned")
}

// |U| <= 1: глобальная проверка не нужна, достаточно dead-end.
func TestAdvanced_SingleCellSkipsGlobalCheck(t *testing.T) {
	g := graph.New(8)
	p := NewAdvancedPruner(g)

	// Осталась одна клетка-сосед — это завершающий ход, не тупик.
	u := state.Bit(17)
	assert.False(t, p.ShouldPruneAfterVisit(0, u))
}

// naiveShouldPrune — справочная реализация семантики AdvancedPruner
// (BFS по спискам соседей, явный подсчёт степеней).
func naiveShouldPrune(g *graph.Graph, last int, unvisited state.State) bool {
	if unvisited.IsEmpty() {
		return false
	}

	// Локальный dead-end (как в DeadEndPruner).
	if unvisited.CountBits() == 1 {
		lone := int(unvisited.TrailingZeroBits())
		return g.GetNeighborMask(lone).Intersect(state.Bit(last)).IsEmpty()
	}
	for u := range g.GetNeighborMask(last).Intersect(unvisited).AllVisited() {
		if g.GetNeighborMask(u).Intersect(unvisited).IsEmpty() {
			return true
		}
	}

	count := unvisited.CountBits()
	if count <= 1 {
		return false
	}

	// Глобальные проверки.
	if g.GetNeighborMask(last).Intersect(unvisited).IsEmpty() {
		return true
	}

	degree := func(v int) int {
		d := 0
		for _, n := range g.GetNeighbors(v) {
			if unvisited.IsVisited(n) {
				d++
			}
		}
		return d
	}

	// BFS от младшего бита.
	seed := int(unvisited.TrailingZeroBits())
	seen := map[int]bool{seed: true}
	queue := []int{seed}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, n := range g.GetNeighbors(v) {
			if unvisited.IsVisited(n) && !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	if len(seen) != count {
		return true
	}

	var deg1 []int
	for u := range unvisited.AllVisited() {
		if degree(u) == 1 {
			deg1 = append(deg1, u)
		}
	}
	if len(deg1) > 2 {
		return true
	}
	if len(deg1) == 2 {
		mask := state.NewState(deg1...)
		if g.GetNeighborMask(last).Intersect(mask).IsEmpty() {
			return true
		}
	}
	return false
}

func TestAdvanced_MatchesNaive(t *testing.T) {
	g := graph.New(5)
	p := NewAdvancedPruner(g)
	totalCells := g.GetTotalCells()

	rng := rand.New(rand.NewSource(42))

	for range 20000 {
		count := 1 + rng.Intn(totalCells)
		u := state.State(0)
		for u.CountBits() < count {
			u = u.Visit(rng.Intn(totalCells))
		}
		last := rng.Intn(totalCells)

		assert.Equal(t, naiveShouldPrune(g, last, u), p.ShouldPruneAfterVisit(last, u),
			"last=%d unvisited=%s", last, u.String())
	}
}

// Сравнение на реалистичных состояниях: случайные легальные пути.
func TestAdvanced_MatchesNaiveOnWalks(t *testing.T) {
	g := graph.New(5)
	p := NewAdvancedPruner(g)
	totalCells := g.GetTotalCells()
	fullMask := state.State(uint64(1)<<uint(totalCells) - 1)

	rng := rand.New(rand.NewSource(7))

	for range 2000 {
		st := state.State(0).Visit(rng.Intn(totalCells))
		end := int(st.TrailingZeroBits())
		for range totalCells - 1 {
			unvisited := fullMask.AndNot(st)
			nbrs := g.GetNeighborMask(end).Intersect(unvisited)
			if nbrs.IsEmpty() {
				break
			}
			n := int(nbrs.TrailingZeroBits())
			childUnvisited := unvisited.Unvisit(n)

			assert.Equal(t, naiveShouldPrune(g, n, childUnvisited), p.ShouldPruneAfterVisit(n, childUnvisited),
				"last=%d unvisited=%s", n, childUnvisited.String())

			st = st.Visit(n)
			end = n
		}
	}
}
