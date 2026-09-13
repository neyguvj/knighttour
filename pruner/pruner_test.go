package pruner

import (
	"math/bits"
	"math/rand"
	"testing"

	"knighttour/graph"
	"knighttour/state"

	"github.com/stretchr/testify/assert"
)

func shouldPrune(p *Pruner, last int, unvisited state.State) bool {
	pruned, _ := p.ShouldPruneAfterVisit(last, unvisited)
	return pruned
}

// --- local dead-end cases (migrated from the former DeadEndPruner tests) ---

func TestShouldPruneAfterVisit_NoUnvisited(t *testing.T) {
	g := graph.New(5)
	p := New(g)

	pruned, reason := p.ShouldPruneAfterVisit(0, state.State(0))
	assert.False(t, pruned, "No unvisited cells should not be pruned")
	assert.Equal(t, NoReason, reason)
}

func TestShouldPruneAfterVisit_SingleUnvisitedReachable(t *testing.T) {
	g := graph.New(5)
	p := New(g)

	pruned, reason := p.ShouldPruneAfterVisit(24, state.Bit(13))
	assert.False(t, pruned, "Single reachable unvisited cell should not be pruned")
	assert.Equal(t, NoReason, reason)
}

func TestShouldPruneAfterVisit_SingleUnvisitedUnreachable(t *testing.T) {
	g := graph.New(5)
	p := New(g)

	pruned, reason := p.ShouldPruneAfterVisit(23, state.Bit(24))
	assert.True(t, pruned, "Single unreachable unvisited cell should be pruned")
	assert.Equal(t, DeadEnd, reason)
}

func TestShouldPruneAfterVisit_IsolatedVertexCorner(t *testing.T) {
	g := graph.New(5)
	p := New(g)

	// Unvisited 0, 2, 4, 6; cell 0 (corner) is isolated and adjacent to last=7.
	pruned, reason := p.ShouldPruneAfterVisit(7, state.NewState(0, 2, 4, 6))
	assert.True(t, pruned, "Isolated vertex at corner should be pruned")
	assert.Equal(t, DeadEnd, reason)
}

func TestShouldPruneAfterVisit_IsolatedVertexCenter(t *testing.T) {
	g := graph.New(5)
	p := New(g)

	// Only cell 12 (center) remains, not adjacent to last=24.
	pruned, reason := p.ShouldPruneAfterVisit(24, state.Bit(12))
	assert.True(t, pruned, "Isolated vertex at center should be pruned")
	assert.Equal(t, DeadEnd, reason)
}

func TestShouldPruneAfterVisit_TwoIsolatedVertices(t *testing.T) {
	g := graph.New(5)
	p := New(g)

	// Cells 0 and 24 are isolated from each other; 0 is adjacent to last=7.
	pruned, reason := p.ShouldPruneAfterVisit(7, state.NewState(0, 24))
	assert.True(t, pruned, "Two isolated vertices should be pruned")
	assert.Equal(t, DeadEnd, reason)
}

func TestShouldPruneAfterVisit_ValidPathNotPruned(t *testing.T) {
	g := graph.New(5)
	p := New(g)
	totalCells := g.GetTotalCells()
	fullMask := state.State(uint64(1)<<uint(totalCells) - 1)

	assert.False(t, shouldPrune(p, 1, fullMask.AndNot(state.NewState(0, 1))), "Valid path should not be pruned")
}

// fullScanShouldPrune is the reference implementation scanning all unvisited cells.
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
	p := New(g)
	totalCells := g.GetTotalCells()
	fullMask := state.State(uint64(1)<<uint(totalCells) - 1)

	rng := rand.New(rand.NewSource(42))

	for trial := range 2000 {
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
				break // invariant broken below; equivalence not guaranteed
			}
			want := fullScanShouldPrune(g, int(n), childUnvisited)
			got, reason := p.ShouldPruneAfterVisit(int(n), childUnvisited)

			if want {
				assert.True(t, got, "trial %d step %d: full scan prunes but merged pruner does not", trial, step)
				assert.Equal(t, DeadEnd, reason, "trial %d step %d: isolation must be reported as DeadEnd", trial, step)
			} else if got {
				// Global checks may prune beyond the full-scan result, but a
				// dead-end under an intact invariant must never fire falsely.
				assert.NotEqual(t, DeadEnd, reason, "trial %d step %d: spurious dead-end", trial, step)
			}

			st = newSt
			end = int(n)
		}
	}
}

// --- global checks (migrated from the former advanced_test.go) ---

func TestAdvanced_NoUnvisited(t *testing.T) {
	g := graph.New(8)
	p := New(g)

	assert.False(t, shouldPrune(p, 0, state.State(0)))
}

// U = {0,17} (connected pair) + {7,22} (separate component); last=10 adjacent to 0.
func TestAdvanced_DisconnectedRemainder(t *testing.T) {
	g := graph.New(8)
	p := New(g)

	u := state.NewState(0, 17, 7, 22)
	pruned, reason := p.ShouldPruneAfterVisit(10, u)
	assert.True(t, pruned, "disconnected remainder should be pruned")
	assert.Equal(t, Disconnected, reason)
}

// Chain 0-17-34, last=10 adjacent to 0: connected, two endpoints, one reachable.
func TestAdvanced_ConnectedNotPruned(t *testing.T) {
	g := graph.New(8)
	p := New(g)

	u := state.NewState(0, 17, 34)
	pruned, reason := p.ShouldPruneAfterVisit(10, u)
	assert.False(t, pruned, "connected remainder with reachable endpoint should not be pruned")
	assert.Equal(t, NoReason, reason)
}

// Star: center 17 and three leaves {0,2,34}; 3 degree-1 vertices -> prune.
func TestAdvanced_ThreeDegreeOneVertices(t *testing.T) {
	g := graph.New(8)
	p := New(g)

	u := state.NewState(0, 2, 34, 17)
	pruned, reason := p.ShouldPruneAfterVisit(27, u)
	assert.True(t, pruned, "three degree-1 vertices should be pruned")
	assert.Equal(t, Endpoints, reason)
}

// Chain 0-17-34, last=27 adjacent to the center only: neither forced endpoint
// is reachable from last -> prune.
func TestAdvanced_ForcedEndpointsUnreachable(t *testing.T) {
	g := graph.New(8)
	p := New(g)

	u := state.NewState(0, 17, 34)
	pruned, reason := p.ShouldPruneAfterVisit(27, u)
	assert.True(t, pruned, "unreachable forced endpoints should be pruned")
	assert.Equal(t, Endpoints, reason)
}

// No neighbor among the unvisited cells — no continuation exists.
func TestAdvanced_NoUnvisitedNeighbor(t *testing.T) {
	g := graph.New(8)
	p := New(g)

	// last=0; cells 7 and 22 are not adjacent to 0 (but connected between themselves).
	u := state.NewState(7, 22)
	pruned, reason := p.ShouldPruneAfterVisit(0, u)
	assert.True(t, pruned, "no continuation should be pruned")
	assert.Equal(t, NoContinuation, reason)
}

// |U| <= 1: no global check needed, the dead-end suffices.
func TestAdvanced_SingleCellSkipsGlobalCheck(t *testing.T) {
	g := graph.New(8)
	p := New(g)

	// A single adjacent cell left is the finishing move, not a dead end.
	assert.False(t, shouldPrune(p, 0, state.Bit(17)))
}

func TestPruneReasons(t *testing.T) {
	tests := []struct {
		name      string
		size      int
		last      int
		unvisited state.State
		wantPrune bool
		want      Reason
	}{
		{name: "no unvisited", size: 8, last: 0, unvisited: state.State(0), wantPrune: false, want: NoReason},
		{name: "lone unreachable cell", size: 5, last: 23, unvisited: state.Bit(24), wantPrune: true, want: DeadEnd},
		{name: "isolated neighbor", size: 5, last: 7, unvisited: state.NewState(0, 2, 4, 6), wantPrune: true, want: DeadEnd},
		{name: "no continuation", size: 8, last: 0, unvisited: state.NewState(7, 22), wantPrune: true, want: NoContinuation},
		{name: "disconnected remainder", size: 8, last: 10, unvisited: state.NewState(0, 17, 7, 22), wantPrune: true, want: Disconnected},
		{name: "three degree-1 vertices", size: 8, last: 27, unvisited: state.NewState(0, 2, 34, 17), wantPrune: true, want: Endpoints},
		{name: "unreachable endpoints", size: 8, last: 27, unvisited: state.NewState(0, 17, 34), wantPrune: true, want: Endpoints},
		{name: "healthy chain", size: 8, last: 10, unvisited: state.NewState(0, 17, 34), wantPrune: false, want: NoReason},
	}

	pruners := make(map[int]*Pruner)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := pruners[tc.size]
			if !ok {
				p = New(graph.New(tc.size))
				pruners[tc.size] = p
			}
			pruned, reason := p.ShouldPruneAfterVisit(tc.last, tc.unvisited)
			assert.Equal(t, tc.wantPrune, pruned)
			assert.Equal(t, tc.want, reason)
		})
	}
}

// naiveShouldPrune is the reference implementation of the pruning semantics
// (BFS over neighbor lists, explicit degree counting).
func naiveShouldPrune(g *graph.Graph, last int, unvisited state.State) bool {
	if unvisited.IsEmpty() {
		return false
	}

	// Local dead-end.
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

	// Global checks.
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

	// BFS from the lowest bit.
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
	p := New(g)
	totalCells := g.GetTotalCells()

	rng := rand.New(rand.NewSource(42))

	for range 20000 {
		count := 1 + rng.Intn(totalCells)
		u := state.State(0)
		for u.CountBits() < count {
			u = u.Visit(rng.Intn(totalCells))
		}
		last := rng.Intn(totalCells)

		assert.Equal(t, naiveShouldPrune(g, last, u), shouldPrune(p, last, u),
			"last=%d unvisited=%s", last, u.String())
	}
}

// Comparison on realistic states: random legal walks.
func TestAdvanced_MatchesNaiveOnWalks(t *testing.T) {
	g := graph.New(5)
	p := New(g)
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

			assert.Equal(t, naiveShouldPrune(g, n, childUnvisited), shouldPrune(p, n, childUnvisited),
				"last=%d unvisited=%s", n, childUnvisited.String())

			st = st.Visit(n)
			end = n
		}
	}
}
