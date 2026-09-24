package pruner

import (
	"math/bits"
	"math/rand"
	"testing"

	"knighttour/graph"
	"knighttour/state"
	"knighttour/symmetry"

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

// --- write gate: forced-chain check (specs/pruner.md, ADR-030) ---

// walkState produces a gate-shaped state (cur, todo): a random walk of the
// given length is grown and then descended randomly for depth steps, mimicking
// the generation leaves that feed ShouldPruneState.
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
		{name: "clean state survives", cur: 26, todo: state.State(0x8855cc8c), wantReason: NoReason},
		{name: "forced chain", cur: 29, todo: state.State(0x311198260), wantReason: ForcedChain},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pruned, reason := p.ShouldPruneState(tt.cur, tt.todo)
			assert.Equal(t, tt.wantReason != NoReason, pruned)
			assert.Equal(t, tt.wantReason, reason)

			// The gate body assumes the caller already passed L0/L1 on the
			// descent — pin that these states sit inside the contract.
			basePruned, baseReason := p.ShouldPruneAfterVisit(tt.cur, tt.todo)
			assert.False(t, basePruned, "precondition: L0/L1 must pass, got %v", baseReason)

			if pruned {
				assert.Zero(t, bruteH(g, tt.cur, tt.todo), "a cut state must have no completions")
			}
		})
	}
}

// Gate cuts must never remove a key with nonzero completions — the soundness
// lemma behind the write gate (specs/searcher.md), verified on walk samples
// against the brute-force oracle. The cut reason is always ForcedChain, and
// it must actually fire on the sample.
func TestWriteGateCutsAreSound(t *testing.T) {
	rng := rand.New(rand.NewSource(123)) //nolint:gosec // deterministic test seed
	for _, size := range []int{5, 6} {
		g := graph.New(size)
		p := New(g)
		cuts := 0
		for range 10000 {
			cur, todo, ok := walkState(rng, g, 10+rng.Intn(7), rng.Intn(5))
			if !ok {
				continue
			}
			pruned, reason := p.ShouldPruneState(cur, todo)
			if !pruned {
				continue
			}
			cuts++
			assert.Equal(t, ForcedChain, reason, "the gate has a single reason")
			assert.Zero(t, bruteH(g, cur, todo),
				"size=%d gate cut of a state with completions: cur=%d todo=%s", size, cur, todo.String())
		}
		assert.Positive(t, cuts, "forced chain must fire on size %d sample", size)
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

// countDegenerate checks one (cur, todo) with |todo| ≤ 3 against the gate — a
// firing cut must be a true zero of the brute-force oracle — and counts the
// reachable states that stayed alive for the non-vacuity pin.
func countDegenerate(t *testing.T, p *Pruner, g *graph.Graph, cur int, todo state.State, alive *int) {
	t.Helper()
	cut, reason := p.ShouldPruneState(cur, todo)
	reachable := bruteH(g, cur, todo) > 0
	if cut {
		assert.False(t, reachable, "degenerate cut of a reachable state: cur=%d todo=%s (%v)",
			cur, todo.String(), reason)
	}
	if reachable {
		*alive++
	}
}

// Degenerate remainders (|todo| ≤ 3) run the gate on the general grounds: a
// firing cut stays sound and every reachable state stays alive. Exhaustive
// over cur × todo of size ≤ 3 on 5×5.
func TestShouldPruneState_DegenerateRemainders(t *testing.T) {
	g := graph.New(5)
	p := New(g)
	total := g.GetTotalCells()

	alive := 0
	for cur := range total {
		var cells []int
		for c := range total {
			if c != cur {
				cells = append(cells, c)
			}
		}
		countDegenerate(t, p, g, cur, state.State(0), &alive)
		for i := range cells {
			countDegenerate(t, p, g, cur, state.Bit(cells[i]), &alive)
			for j := i + 1; j < len(cells); j++ {
				countDegenerate(t, p, g, cur, state.Bit(cells[i]).Visit(cells[j]), &alive)
				for k := j + 1; k < len(cells); k++ {
					countDegenerate(t, p, g, cur, state.Bit(cells[i]).Visit(cells[j]).Visit(cells[k]), &alive)
				}
			}
		}
	}
	assert.Positive(t, alive, "reachable degenerate states must stay alive")
}

// mapPos applies a D4 transform to one board position.
func mapPos(tf symmetry.Transform, size, pos int) int {
	x, y := pos/size, pos%size
	nx, ny := tf(x, y, size)
	return nx*size + ny
}

// The gate predicate is D4-equivariant: it depends only on graph structure and
// mask (the forced-chain condition is component-local, no connectivity needed),
// so "dead" is an orbit property and the verdict is consistent with key
// canonicalization (specs/pruner.md).
func TestShouldPruneState_D4Equivariant(t *testing.T) {
	rng := rand.New(rand.NewSource(5)) //nolint:gosec // deterministic test seed
	for _, size := range []int{5, 6} {
		g := graph.New(size)
		p := New(g)
		tfs := symmetry.GetSymmetries(size)

		fired := 0
		for range 20000 {
			cur, todo, ok := walkState(rng, g, 8+rng.Intn(14), rng.Intn(5))
			if !ok {
				continue
			}
			wantPruned, wantReason := p.ShouldPruneState(cur, todo)
			if wantPruned {
				fired++
			}
			for _, tf := range tfs {
				tCur := mapPos(tf, size, cur)
				var tTodo state.State
				for v := range todo.AllVisited() {
					tTodo = tTodo.Visit(mapPos(tf, size, v))
				}
				gotPruned, gotReason := p.ShouldPruneState(tCur, tTodo)
				assert.Equal(t, wantPruned, gotPruned, "cur=%d todo=%s", cur, todo.String())
				if wantPruned {
					assert.Equal(t, wantReason, gotReason, "cur=%d todo=%s", cur, todo.String())
				}
			}
		}
		assert.Positive(t, fired, "sample must contain gate cuts on size %d", size)
	}
}
