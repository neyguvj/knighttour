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
	ForcedChain                  // write gate: forced edges demand path degree > 2 or form a cycle
)

// Pruner combines the cheap local dead-end check with global checks over the
// unvisited subgraph: connectivity and a Hamiltonian-path endpoint heuristic
// (vertices of degree <= 1 must become path endpoints), plus the forced-chain
// write gate. It is stateless and safe for concurrent use — pruning statistics
// live in types.Result.
type Pruner struct {
	graph *graph.Graph
}

// New returns a Pruner with the L0/L1 checks always on and the forced-chain
// write gate (specs/pruner.md).
func New(g *graph.Graph) *Pruner {
	return &Pruner{graph: g}
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

// ShouldPruneState reports whether no Hamiltonian path starting at cur can
// cover todo — the write gate on generation leaves (specs/searcher.md). The
// single accepted check is the forced-chain condition over H = G[todo ∪ {cur}]
// (ADR-030); L0/L1 are deliberately not repeated — the caller's descent has
// already passed the pair through ShouldPruneAfterVisit.
func (p *Pruner) ShouldPruneState(cur int, todo state.State) (bool, Reason) {
	if p.forcedChainCut(cur, todo) {
		return true, ForcedChain
	}
	return false, NoReason
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
//
//nolint:cyclop // hot path: evaluated per generation leaf (ADR-030); splitting the traversal would duplicate work on every call.
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

	for v := range h.AllVisited() {
		fv := forcedVertex(&deg, cur, t, v)
		for w := range p.graph.GetNeighborMask(v).Intersect(h).AllVisited() {
			if w <= v || (!fv && !forcedVertex(&deg, cur, t, w)) {
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

// forcedVertex reports whether v's incident H-edges are all forced onto the
// path: endpoints cur and t use exactly one edge, every other todo vertex is
// internal and forces both of its edges once its degree drops to two.
func forcedVertex(deg *[64]int8, cur, t, v int) bool {
	if v == cur || v == t {
		return deg[v] == 1
	}
	return deg[v] == 2
}

// ufFind is union-find find with path halving over a fixed local array.
func ufFind(uf *[64]int8, v int) int {
	for int(uf[v]) != v {
		uf[v] = uf[uf[v]]
		v = int(uf[v])
	}
	return v
}
