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
)

// Pruner combines the cheap local dead-end check with global checks over the
// unvisited subgraph: connectivity and a Hamiltonian-path endpoint heuristic
// (vertices of degree <= 1 must become path endpoints). It is stateless and
// safe for concurrent use; pruning statistics live in types.Result.
type Pruner struct {
	graph *graph.Graph
}

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
