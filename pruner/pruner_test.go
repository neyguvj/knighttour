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

// --- L2: ShouldPruneState (plan 04) ---

// walkState produces a DP-like state (cur, todo): a random walk of the given
// length is grown and then descended randomly for depth steps, mimicking the
// states the shapecount DP feeds into the pruner.
func walkState(rng *rand.Rand, g *graph.Graph, length, descend int) (int, state.State, bool) {
	var mask state.State
	for range 100 {
		mask = state.Bit(rng.Intn(g.GetTotalCells()))
		cur := bits.TrailingZeros64(uint64(mask))
		grew := true
		for range length - 1 {
			var cand []int
			for n := range g.GetNeighborMask(cur).AllVisited() {
				if mask.IsUnvisited(n) {
					cand = append(cand, n)
				}
			}
			if len(cand) == 0 {
				grew = false
				break
			}
			cur = cand[rng.Intn(len(cand))]
			mask = mask.Visit(cur)
		}
		if grew && mask.CountBits() == length {
			break
		}
	}
	if mask.CountBits() != length {
		return 0, 0, false
	}
	cur := bits.TrailingZeros64(uint64(mask))
	todo := mask.Unvisit(cur)
	for range descend {
		var cand []int
		for n := range g.GetNeighborMask(cur).Intersect(todo).AllVisited() {
			cand = append(cand, n)
		}
		if len(cand) == 0 {
			break
		}
		cur = cand[rng.Intn(len(cand))]
		todo = todo.Unvisit(cur)
	}
	return cur, todo, true
}

// naiveArtic replicates the L2 articulation semantics from scratch: component
// counts via BFS after every removal plus the fixed-end refinements. Only
// valid when H = todo ∪ {cur} is connected (guaranteed once L0/L1 pass).
func naiveArtic(g *graph.Graph, cur int, todo state.State) bool {
	h := todo | state.Bit(cur)
	components := func(mask state.State) (int, map[int]int) {
		id := map[int]int{}
		count, n := 0, 0
		var seen state.State
		for v := range mask.AllVisited() {
			if _, ok := id[v]; ok {
				continue
			}
			count++
			frontier := state.Bit(v)
			for !frontier.IsEmpty() {
				var next state.State
				for u := range frontier.AllVisited() {
					if seen.IsVisited(u) {
						continue
					}
					seen = seen.Visit(u)
					id[u] = n
					next = next.Union(g.GetNeighborMask(u).Intersect(mask))
				}
				frontier = next.AndNot(seen)
			}
			n++
		}
		return count, id
	}
	deg1Count, t := 0, -1
	for v := range todo.AllVisited() {
		if g.GetNeighborMask(v).Intersect(h).CountBits() == 1 {
			deg1Count++
			t = v
		}
	}
	for v := range h.AllVisited() {
		c, ids := components(h.Unvisit(v))
		if c >= 3 {
			return true
		}
		if deg1Count != 1 || c < 2 {
			continue
		}
		if v == cur || v == t { // removing a path endpoint must leave one segment
			return true
		}
		if ids[cur] == ids[t] { // both endpoints on the same side of the split
			return true
		}
	}
	return false
}

// bruteH counts Hamiltonian paths from cur covering todo (reference oracle).
func bruteH(g *graph.Graph, cur int, todo state.State) uint64 {
	if todo.IsEmpty() {
		return 1
	}
	var total uint64
	for n := range g.GetNeighborMask(cur).Intersect(todo).AllVisited() {
		total += bruteH(g, n, todo.Unvisit(n))
	}
	return total
}

func TestShouldPruneState_Reasons(t *testing.T) {
	g := graph.New(6)
	p := New(g)

	tests := []struct {
		name       string
		cur        int
		todo       state.State
		wantReason Reason
	}{
		{"clean state survives", 26, state.State(0x8855cc8c), NoReason},
		{"articulation point", 19, state.State(0x424318b64), Articulation},
		{"forced chain", 29, state.State(0x311198260), ForcedChain},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.GreaterOrEqual(t, tt.todo.CountBits(), DefaultMinL2)
			p.SetL2(L2All, DefaultMinL2)
			pruned, reason := p.ShouldPruneState(tt.cur, tt.todo)
			assert.Equal(t, tt.wantReason != NoReason, pruned)
			assert.Equal(t, tt.wantReason, reason)

			// With L2 disabled the same state must not be cut (L0/L1 pass).
			p.SetL2(L2None, DefaultMinL2)
			pruned, reason = p.ShouldPruneState(tt.cur, tt.todo)
			assert.False(t, pruned)
			assert.Equal(t, NoReason, reason)
		})
	}
}

func TestShouldPruneState_BelowThresholdMatchesBase(t *testing.T) {
	g := graph.New(6)
	p := New(g)
	p.SetL2(L2All, 1000) // unreachable threshold: L2 must never run

	rng := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test seed
	for range 5000 {
		cur, todo, ok := walkState(rng, g, 12+rng.Intn(7), rng.Intn(3))
		if !ok {
			continue
		}
		wantPruned, wantReason := p.ShouldPruneAfterVisit(cur, todo)
		gotPruned, gotReason := p.ShouldPruneState(cur, todo)
		assert.Equal(t, wantPruned, gotPruned)
		assert.Equal(t, wantReason, gotReason)
	}
}

func TestArticulationCut_MatchesNaive(t *testing.T) {
	g := graph.New(6)
	p := New(g)

	rng := rand.New(rand.NewSource(11)) //nolint:gosec // deterministic test seed
	compared := 0
	for range 20000 {
		cur, todo, ok := walkState(rng, g, 12+rng.Intn(7), rng.Intn(5))
		if !ok || todo.CountBits() < 2 {
			continue
		}
		if pruned, _ := p.ShouldPruneAfterVisit(cur, todo); pruned {
			continue // articulationCut assumes the L0/L1 guarantees (connected H)
		}
		assert.Equal(t, naiveArtic(g, cur, todo), p.articulationCut(cur, todo),
			"cur=%d todo=%s", cur, todo.String())
		compared++
	}
	assert.Greater(t, compared, 5000)
}

// L2 cuts must never remove a state with a nonzero completion count.
func TestL2_CutsAreSoundOnRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(123)) //nolint:gosec // deterministic test seed
	for _, size := range []int{5, 6} {
		g := graph.New(size)
		p := New(g)
		p.SetL2(L2All, 8)
		hits := map[Reason]int{}
		for range 10000 {
			cur, todo, ok := walkState(rng, g, 10+rng.Intn(7), rng.Intn(5))
			if !ok {
				continue
			}
			pruned, reason := p.ShouldPruneState(cur, todo)
			if !pruned || (reason != Articulation && reason != ForcedChain) {
				continue
			}
			hits[reason]++
			assert.Zero(t, bruteH(g, cur, todo),
				"size=%d L2 cut (%v) of a state with completions: cur=%d todo=%s",
				size, reason, cur, todo.String())
		}
		assert.Positive(t, hits[Articulation], "articulation must fire on size %d sample", size)
		assert.Positive(t, hits[ForcedChain], "forced chain must fire on size %d sample", size)
	}
}

// Forced-chain forcing is only sound with a pinned end; states without a
// unique degree-1 todo vertex must never be cut by it.
func TestForcedChainCut_RequiresFixedEnd(t *testing.T) {
	g := graph.New(6)
	p := New(g)

	rng := rand.New(rand.NewSource(9)) //nolint:gosec // deterministic test seed
	for range 20000 {
		cur, todo, ok := walkState(rng, g, 12+rng.Intn(7), rng.Intn(5))
		if !ok {
			continue
		}
		if !p.forcedChainCut(cur, todo) {
			continue
		}
		h := todo | state.Bit(cur)
		deg1 := 0
		for v := range todo.AllVisited() {
			if g.GetNeighborMask(v).Intersect(h).CountBits() == 1 {
				deg1++
			}
		}
		assert.Equal(t, 1, deg1, "cur=%d todo=%s", cur, todo.String())
	}
}

// Shape-level feasibility filter (plan 02): table cases per check plus the
// degenerate shapes; every cut must be a true zero of the brute-force oracle.
func TestShapeFeasible_Reasons(t *testing.T) {
	g := graph.New(6)
	p := New(g)

	star := state.NewState(14, 1, 3, 6) // K1,3 in the 6×6 knight graph (center 14)

	tests := []struct {
		name       string
		end        int
		shape      state.State
		mask       L2Checks
		wantReason Reason
	}{
		{"endpoint mismatch star center", 14, star, L2Endpoints, Endpoints},
		{"star leaf articulation", 1, star, L2Articulation, Articulation},
		{"forced chain shape", 29, state.State(0x311198260) | state.Bit(29), L2ForcedChain, ForcedChain},
		{"articulation shape", 19, state.State(0x424318b64) | state.Bit(19), L2Articulation, Articulation},
		{"single cell alive", 5, state.Bit(5), L2All | L2Endpoints, NoReason},
		{"adjacent pair alive", 0, state.NewState(0, 8), L2All | L2Endpoints, NoReason},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pruned, reason := p.ShapeFeasible(tt.end, tt.shape, tt.mask)
			assert.Equal(t, tt.wantReason != NoReason, pruned)
			assert.Equal(t, tt.wantReason, reason)
			if pruned {
				assert.Zero(t, bruteH(g, tt.end, tt.shape.Unvisit(tt.end)),
					"filter cut a shape with completions: end=%d shape=%s", tt.end, tt.shape.String())
			}
		})
	}
}

// The filter must never cut an end that has completions — random states from
// the DP-shaped walk generator verified against bruteH.
func TestShapeFeasible_SoundOnRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // deterministic test seed
	for _, size := range []int{5, 6} {
		g := graph.New(size)
		p := New(g)
		killed := 0
		for range 20000 {
			cur, todo, ok := walkState(rng, g, 8+rng.Intn(14), rng.Intn(5))
			if !ok {
				continue
			}
			shape := todo | state.Bit(cur)
			if pruned, _ := p.ShapeFeasible(cur, shape, L2All|L2Endpoints); pruned {
				killed++
				assert.Zero(t, bruteH(g, cur, todo),
					"size=%d false cut: cur=%d shape=%s", size, cur, shape.String())
			}
		}
		assert.Positive(t, killed, "filter must fire on the size %d sample", size)
	}
}
