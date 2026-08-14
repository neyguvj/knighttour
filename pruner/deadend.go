package pruner

import (
	"knighttour/graph"
	"knighttour/path"
	"knighttour/state"
)

type DeadEndPruner struct {
	graph *graph.Graph
}

func NewDeadEndPruner(graph *graph.Graph) *DeadEndPruner {
	return &DeadEndPruner{graph: graph}
}

func (p *DeadEndPruner) ShouldPrune(path path.Path) bool {
	totalCells := p.graph.GetTotalCells()
	s := path.State()
	unvisitedMask := s.UnvisitedMask(totalCells)
	if unvisitedMask.IsEmpty() {
		return false
	}

	if unvisitedMask.CountBits() == 1 {
		lastPos := int(unvisitedMask.TrailingZeroBits())
		currentMask := state.NewState().Visit(path.End())
		if p.graph.GetNeighborMask(lastPos).Intersect(currentMask).IsEmpty() {
			return true
		}
		return false
	}

	for i := 0; i < totalCells; i++ {
		if s.IsUnvisited(i) {
			neighborMask := p.graph.GetNeighborMask(i)
			if neighborMask.Intersect(unvisitedMask).IsEmpty() {
				return true
			}
		}
	}

	return false
}
