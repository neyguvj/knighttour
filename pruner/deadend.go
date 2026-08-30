package pruner

import (
	"knighttour/graph"
	"knighttour/state"
)

type DeadEndPruner struct {
	graph *graph.Graph
}

func NewDeadEndPruner(g *graph.Graph) *DeadEndPruner {
	return &DeadEndPruner{
		graph: g,
	}
}

func (p *DeadEndPruner) ShouldPruneAfterVisit(last int, unvisited state.State) bool {
	if unvisited.IsEmpty() {
		return false
	}

	if unvisited.CountBits() == 1 {
		lone := int(unvisited.TrailingZeroBits())
		return p.graph.GetNeighborMask(lone).Intersect(state.Bit(last)).IsEmpty()
	}

	for u := range p.graph.GetNeighborMask(last).Intersect(unvisited).AllVisited() {
		if p.graph.GetNeighborMask(u).Intersect(unvisited).IsEmpty() {
			return true
		}
	}

	return false
}
