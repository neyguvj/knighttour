package pruner

import (
	"math/bits"

	"knighttour/graph"
	"knighttour/state"
)

// Reason identifies which check cut a branch. It is returned to the caller so
// statistics can be attributed per subtask without shared counters in Pruner.
type Reason uint8

const (
	NoReason       Reason = iota // branch was not pruned
	DeadEnd                      // local dead-end: isolated cell / lone unreachable cell
	NoContinuation               // last has no unvisited neighbor
	Disconnected                 // G[unvisited] is not a single component
	Endpoints                    // degree-1 endpoint heuristic violated
	Articulation                 // L2: a vertex of H splits it into 3+ components (2 with fixed end)
	ForcedChain                  // L2: forced edges demand path degree > 2 or form a cycle
)

// L2Checks selects the enhanced necessary-condition checks over the state graph
// H = G[todo ∪ {cur}] used by ShouldPruneState (final DP only, plan 04). It is
// a bitmask so individual checks can be measured independently.
type L2Checks uint8

const (
	L2Articulation L2Checks = 1 << iota // articulation points (Tarjan lowlink)
	L2ForcedChain                       // forced chains of degree-2 vertices

	// L2Endpoints selects only the shape-level endpoint-mismatch check
	// (ShapeFeasible); meaningless for ShouldPruneState.
	L2Endpoints
)

const (
	L2None L2Checks = 0
	L2All           = L2Articulation | L2ForcedChain

	// DefaultMinL2 is the remainder threshold for the L2 checks when they are
	// enabled: below it a full descent is cheaper than an O(V+E) verification.
	DefaultMinL2 = 10
)

// Pruner combines the cheap local dead-end check with global checks over the
// unvisited subgraph: connectivity and a Hamiltonian-path endpoint heuristic
// (vertices of degree <= 1 must become path endpoints). The L2 configuration
// is set once before workers start; after that the Pruner is stateless and
// safe for concurrent use — pruning statistics live in types.Result.
type Pruner struct {
	graph *graph.Graph
	l2    L2Checks
	minL2 int
}

// New returns a Pruner with the L0/L1 checks always on and L2 off: the plan-04
// stage-0 measurements (specs/plans/04-pruner-cut-vertices.md) showed the O(V+E)
// per-state cost outweighs the DP shrink at current board sizes. Enable via
// SetL2 for experiments.
func New(g *graph.Graph) *Pruner {
	return &Pruner{graph: g, l2: L2None, minL2: DefaultMinL2}
}

// SetL2 configures the enhanced DP checks (mask) and the remainder threshold
// below which they are skipped. Call it at construction time, before any
// concurrent use.
func (p *Pruner) SetL2(mask L2Checks, minTodo int) {
	p.l2, p.minL2 = mask, minTodo
}

// ShouldPruneAfterVisit reports whether the branch is hopeless right after
// visiting last with unvisited remaining, together with the first reason that
// fired (NoReason when not pruned). The local dead-end runs first; the global
// check runs for every remainder of two or more cells.
func (p *Pruner) ShouldPruneAfterVisit(last int, unvisited state.State) (bool, Reason) {
	if unvisited.IsEmpty() {
		return false, NoReason
	}

	if unvisited.CountBits() == 1 {
		lone := int(unvisited.TrailingZeroBits())
		if p.graph.GetNeighborMask(lone).Intersect(state.Bit(last)).IsEmpty() {
			return true, DeadEnd
		}
		return false, NoReason
	}

	// Visiting last could only isolate one of its own unvisited neighbors.
	for u := range p.graph.GetNeighborMask(last).Intersect(unvisited).AllVisited() {
		if p.graph.GetNeighborMask(u).Intersect(unvisited).IsEmpty() {
			return true, DeadEnd
		}
	}

	return p.globalCheck(last, unvisited)
}

// globalCheck runs bitset flood-fill over G[unvisited] and applies:
//  1. no continuation possible: last has no unvisited neighbor;
//  2. connectivity: all unvisited cells form one component (the remaining
//     path cannot leave a component without revisiting cells);
//  3. endpoint heuristic: >2 degree-1 vertices is impossible; exactly two
//     require at least one to be adjacent to last (the path enters the
//     remainder through it).
func (p *Pruner) globalCheck(last int, unvisited state.State) (bool, Reason) {
	if p.graph.GetNeighborMask(last).Intersect(unvisited).IsEmpty() {
		return true, NoContinuation
	}

	var seen, deg1Mask state.State
	frontier := state.Bit(bits.TrailingZeros64(uint64(unvisited)))
	deg1Count := 0

	for !frontier.IsEmpty() {
		seen = seen.Union(frontier)
		var next state.State
		for u := range frontier.AllVisited() {
			nbrs := p.graph.GetNeighborMask(u).Intersect(unvisited)
			switch nbrs.CountBits() {
			case 0:
				return true, DeadEnd // isolated vertex (normally caught by the local check)
			case 1:
				deg1Count++
				if deg1Count > 2 {
					return true, Endpoints
				}
				deg1Mask = deg1Mask.Visit(u)
			}
			next = next.Union(nbrs)
		}
		frontier = next.AndNot(seen)
	}

	if seen != unvisited {
		return true, Disconnected // disconnected remainder
	}

	if deg1Count == 2 && p.graph.GetNeighborMask(last).Intersect(deg1Mask).IsEmpty() {
		return true, Endpoints // neither forced endpoint is reachable from last
	}
	return false, NoReason
}

// ShapeFeasible reports whether no Hamiltonian path covering the whole shape
// and ending at end exists — a per-shape feasibility filter run before the DP
// starts (plan 02). Checks are selected by mask: A endpoint mismatch over
// G[shape] (L2Endpoints), B forced chains (L2ForcedChain), C articulations
// (L2Articulation). Reasons reuse Endpoints/ForcedChain/Articulation; every
// condition is necessary for a Hamiltonian path, so a true result proves
// h(shape, end) == 0. end must be a member of shape. Connectivity of G[shape]
// is not assumed: on a disconnected shape the checks may or may not fire, and
// any verdict is sound (h == 0).
func (p *Pruner) ShapeFeasible(end int, shape state.State, mask L2Checks) (bool, Reason) {
	// A: degree-1 vertices of G[shape] must be path ends. More than two makes
	// a Hamiltonian path impossible outright; exactly two pin both ends, so a
	// fixed end outside the pair is hopeless. Measured on the real 6×6 d13
	// distribution this never fires beyond the root L1 pass (specs/plans/02),
	// hence it is opt-in and off in the production default mask.
	if mask&L2Endpoints != 0 {
		deg1, deg1Count := p.shapeDeg1(shape)
		if deg1Count > 2 || (deg1Count == 2 && !deg1.IsVisited(end)) {
			return true, Endpoints
		}
	}

	todo := shape.Unvisit(end)
	if mask&L2ForcedChain != 0 && p.forcedChainCut(end, todo) {
		return true, ForcedChain
	}
	if mask&L2Articulation != 0 && p.articulationCut(end, todo) {
		return true, Articulation
	}
	return false, NoReason
}

// shapeDeg1 returns the mask and count of degree-1 vertices of G[shape].
func (p *Pruner) shapeDeg1(shape state.State) (deg1 state.State, count int) {
	for v := range shape.AllVisited() {
		if p.graph.GetNeighborMask(v).Intersect(shape).CountBits() == 1 {
			count++
			deg1 = deg1.Visit(v)
		}
	}
	return deg1, count
}

// ShouldPruneState reports whether no Hamiltonian path starting at cur can
// cover todo. It runs the full L0/L1 pass first and then, when |todo| reaches
// minL2, the configured L2 necessary-condition checks over H = G[todo ∪ {cur}].
// Only the shapecount final DP calls this; generation DFS keeps using
// ShouldPruneAfterVisit (plan 04: L2 pays off only on the exponential DP tree).
func (p *Pruner) ShouldPruneState(cur int, todo state.State) (bool, Reason) {
	if pruned, reason := p.ShouldPruneAfterVisit(cur, todo); pruned {
		return true, reason
	}
	if p.l2 == L2None || todo.CountBits() < p.minL2 {
		return false, NoReason
	}
	if p.l2&L2Articulation != 0 && p.articulationCut(cur, todo) {
		return true, Articulation
	}
	if p.l2&L2ForcedChain != 0 && p.forcedChainCut(cur, todo) {
		return true, ForcedChain
	}
	return false, NoReason
}

// articulationCut runs an iterative Tarjan lowlink over the connected graph
// H = G[todo ∪ {cur}] (connectivity is guaranteed by the L1 pass) and reports
// whether a Hamiltonian path starting at cur is impossible:
//
//   - some vertex splits H into 3+ components — removing a path vertex leaves
//     at most two path segments;
//   - with the end fixed (t = the unique todo vertex of degree 1 in H, which
//     must be the path end): removing an endpoint (cur or t) disconnects H —
//     an endpoint sits at the very edge of the path;
//   - a 2-component split of some v places cur and t on the same side — the
//     other component could only be covered by passing v twice.
//
// All scratch arrays are fixed-size locals (no heap allocation); vertex ids
// index them directly, DFS order fits int8 (|H| ≤ 64).
func (p *Pruner) articulationCut(cur int, todo state.State) bool {
	h := todo | state.Bit(cur)

	var disc, low, children, heavy, lastHeavy, subEnd [64]int8
	var parent [64]int8
	var frontier [64]state.State
	var stack [64]int8
	var rootChild [2]int8

	root := int(bits.TrailingZeros64(uint64(h)))
	parent[root] = -1
	disc[root], low[root] = 1, 1
	nbrs := p.graph.GetNeighborMask(root).Intersect(h)
	frontier[root] = nbrs
	stack[0] = int8(root)
	timer := int8(1)
	top := 0

	deg1Count, deg1Vertex := 0, -1
	if root != cur && nbrs.CountBits() == 1 {
		deg1Count++
		deg1Vertex = root
	}

	for top >= 0 {
		v := int(stack[top])
		m := frontier[v]
		if m != 0 {
			w := bits.TrailingZeros64(uint64(m))
			frontier[v] = m & (m - 1)
			if disc[w] == 0 {
				children[v]++
				if v == root && children[v] <= 2 {
					rootChild[children[v]-1] = int8(w)
				}
				parent[w] = int8(v)
				top++
				stack[top] = int8(w)
				timer++
				disc[w], low[w] = timer, timer
				nbrs := p.graph.GetNeighborMask(w).Intersect(h)
				frontier[w] = nbrs
				if w != cur && nbrs.CountBits() == 1 {
					deg1Count++
					deg1Vertex = w
				}
			} else if w != int(parent[v]) {
				low[v] = min(low[v], disc[w])
			}
			continue
		}
		subEnd[v] = timer
		top--
		if pv := int(parent[v]); pv >= 0 {
			if low[v] >= disc[pv] {
				heavy[pv]++
				lastHeavy[pv] = int8(v)
			}
			low[pv] = min(low[pv], low[v])
		}
	}

	tKnown := deg1Count == 1
	inSubtree := func(u, c int) bool {
		return disc[c] <= disc[u] && disc[u] <= subEnd[c]
	}
	for v := range h.AllVisited() {
		comp := int(heavy[v]) + 1
		if v == root {
			comp = int(children[v])
		}
		if comp >= 3 {
			return true
		}
		if !tKnown || comp < 2 {
			continue
		}
		t := deg1Vertex
		if v == cur || v == t {
			return true // an endpoint removal must leave one segment
		}
		if v == root { // comp == 2: the two child subtrees are the components
			if inSubtree(cur, int(rootChild[0])) == inSubtree(deg1Vertex, int(rootChild[0])) {
				return true
			}
			continue
		}
		if heavy[v] != 1 { // comp == 2 ⇔ exactly one heavy child
			continue
		}
		hc := int(lastHeavy[v])
		if inSubtree(cur, hc) == inSubtree(t, hc) {
			return true
		}
	}
	return false
}

// forcedChainCut applies the forced-edge necessary condition, sound only when
// the path end t is pinned down (the unique todo vertex of degree 1 in H —
// with a free end any chain can be broken by the unknown endpoint). Then every
// other todo vertex is internal (exactly two path edges), cur uses exactly one
// edge and t its only edge, so:
//
//   - both H-edges of each internal degree-2 vertex are forced;
//   - the single edge of t is forced, as is cur's when deg_H(cur) == 1.
//
// Prune when forced degrees exceed the path degree (>2 in todo, >1 at cur) or
// the forced edges contain a cycle (union-find: |E| ≥ |V| in a component).
func (p *Pruner) forcedChainCut(cur int, todo state.State) bool {
	h := todo | state.Bit(cur)

	var deg [64]int8
	deg1Count, t := 0, -1
	for v := range h.AllVisited() {
		d := p.graph.GetNeighborMask(v).Intersect(h).CountBits()
		deg[v] = int8(d)
		if v != cur && d == 1 {
			deg1Count++
			t = v
		}
	}
	if deg1Count != 1 {
		return false // end is not pinned down: forcing would be unsound
	}

	var uf [64]int8
	var verts, edges, fdeg [64]int8
	for v := range h.AllVisited() {
		uf[v] = int8(v)
		verts[v] = 1
	}

	forces := func(v int) bool {
		if v == cur || v == t {
			return deg[v] == 1
		}
		return deg[v] == 2
	}
	for v := range h.AllVisited() {
		fv := forces(v)
		for w := range p.graph.GetNeighborMask(v).Intersect(h).AllVisited() {
			if w <= v || (!fv && !forces(w)) {
				continue // each edge once; only forced edges matter
			}
			fdeg[v]++
			fdeg[w]++
			if fdeg[cur] > 1 || (v != cur && fdeg[v] > 2) || (w != cur && fdeg[w] > 2) {
				return true
			}
			ra, rb := ufFind(&uf, v), ufFind(&uf, w)
			if ra == rb {
				edges[ra]++
				if edges[ra] >= verts[ra] {
					return true // cycle among forced edges
				}
				continue
			}
			uf[rb] = int8(ra)
			verts[ra] += verts[rb]
			edges[ra] += edges[rb] + 1
			if edges[ra] >= verts[ra] {
				return true
			}
		}
	}
	return false
}

// ufFind is union-find find with path halving over a fixed local array.
func ufFind(uf *[64]int8, v int) int {
	for int(uf[v]) != v {
		uf[v] = uf[uf[v]]
		v = int(uf[v])
	}
	return v
}
