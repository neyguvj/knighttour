package pruner

import (
	"math/bits"

	"knighttour/graph"
	"knighttour/state"
)

// AdvancedPruner combines the cheap local dead-end check with global checks
// over the unvisited subgraph: connectivity and a Hamiltonian-path endpoint
// heuristic (vertices of degree <= 1 must become path endpoints).
type AdvancedPruner struct {
	deadend *DeadEndPruner
	graph   *graph.Graph
}

func NewAdvancedPruner(g *graph.Graph) *AdvancedPruner {
	return &AdvancedPruner{
		deadend: NewDeadEndPruner(g),
		graph:   g,
	}
}

// ShouldPruneAfterVisit reports whether the branch is hopeless right after
// visiting last with unvisited remaining. The local dead-end runs first;
// the global check runs for every remainder of two or more cells.
func (p *AdvancedPruner) ShouldPruneAfterVisit(last int, unvisited state.State) bool {
	if p.deadend.ShouldPruneAfterVisit(last, unvisited) {
		return true
	}

	if unvisited.CountBits() > 1 {
		return p.globalCheck(last, unvisited)
	}
	return false
}

// globalCheck runs bitset flood-fill over G[unvisited] and applies:
//  1. no continuation possible: last has no unvisited neighbor;
//  2. connectivity: all unvisited cells form one component (the remaining
//     path cannot leave a component without revisiting cells);
//  3. endpoint heuristic: >2 degree-1 vertices is impossible; exactly two
//     require at least one to be adjacent to last (the path enters the
//     remainder through it).
func (p *AdvancedPruner) globalCheck(last int, unvisited state.State) bool {
	if p.graph.GetNeighborMask(last).Intersect(unvisited).IsEmpty() {
		return true
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
				return true // isolated vertex (normally caught by dead-end)
			case 1:
				deg1Count++
				if deg1Count > 2 {
					return true
				}
				deg1Mask = deg1Mask.Visit(u)
			}
			next = next.Union(nbrs)
		}
		frontier = next.AndNot(seen)
	}

	if seen != unvisited {
		return true // disconnected remainder
	}

	if deg1Count == 2 && p.graph.GetNeighborMask(last).Intersect(deg1Mask).IsEmpty() {
		return true // neither forced endpoint is reachable from last
	}
	return false
}
