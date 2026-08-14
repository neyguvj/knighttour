package graph

import "knighttour/state"

type Graph struct {
	size          int
	neighbors     [][]int
	neighborMasks []state.State
	totalCells    int
}

func New(size int) *Graph {
	g := &Graph{
		size:       size,
		totalCells: size * size,
	}
	g.buildNeighbors()
	return g
}

func (g *Graph) Size() int {
	return g.size
}

func (g *Graph) GetTotalCells() int {
	return g.totalCells
}

func (g *Graph) GetNeighbors(pos int) []int {
	return g.neighbors[pos]
}

func (g *Graph) GetDegree(pos int) int {
	return len(g.neighbors[pos])
}

func (g *Graph) GetNeighborMask(pos int) state.State {
	return g.neighborMasks[pos]
}

func (g *Graph) buildNeighbors() {
	g.neighbors = make([][]int, g.totalCells)
	g.neighborMasks = make([]state.State, g.totalCells)
	for pos := 0; pos < g.totalCells; pos++ {
		x, y := pos/g.size, pos%g.size
		possibleMoves := []struct{ dx, dy int }{
			{-2, -1}, {-2, +1}, {-1, -2}, {-1, +2},
			{+1, -2}, {+1, +2}, {+2, -1}, {+2, +1},
		}
		for _, move := range possibleMoves {
			nx, ny := x+move.dx, y+move.dy
			if nx >= 0 && nx < g.size && ny >= 0 && ny < g.size {
				n := nx*g.size + ny
				g.neighbors[pos] = append(g.neighbors[pos], n)
				g.neighborMasks[pos] = g.neighborMasks[pos].Union(state.State(1 << uint64(n)))
			}
		}
	}
}
