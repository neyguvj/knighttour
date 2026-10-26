// Package pairs computes the matrix N(s,t) of directed Hamiltonian paths of
// the knight graph: the number of paths from cell s to cell t. A matrix row is
// an anchored descent from the start down to full coverage with a histogram of
// the last cell; the full matrix is built over D4 orbit representatives and
// completed by equivariance N(σs,σt) = N(s,t). The package is a standalone
// contour over graph, state, pruner, symmetry and workerpool: it is not
// imported by the counting pipeline (counter) or the CLI, and it touches
// neither searcher nor cache (specs/pairs.md, plan 28).
package pairs

import (
	"context"
	"fmt"
	"iter"

	"knighttour/graph"
	"knighttour/pruner"
	"knighttour/state"
	"knighttour/symmetry"
	"knighttour/workerpool"
)

// Row returns the distribution of directed Hamiltonian path ends from the cell
// start: out[t] = N(start,t). Full descent with pruner cutoffs; no memoization
// and no symmetry reduction inside a row — a fixed start breaks D4. The
// precondition is 0 ≤ start < g.GetTotalCells(). Deterministic; a terminated
// ctx yields a partial vector without panicking.
func Row(ctx context.Context, g *graph.Graph, start int) []uint64 {
	out := make([]uint64, g.GetTotalCells())
	descendRow(ctx, g, pruner.New(g), state.NewState(start), start, out)
	return out
}

// descendRow is the anchored DFS of Row: a full-board leaf adds one to the
// slot of its last cell; candidates and cutoffs mirror the searcher's hot
// descent (pruner after visit, skipped on an empty remainder) but carry no
// statistics — this is the off-pipeline oracle contour.
func descendRow(ctx context.Context, g *graph.Graph, p *pruner.Pruner, st state.State, end int, out []uint64) {
	if ctx.Err() != nil {
		return
	}

	if st.CountBits() == g.GetTotalCells() {
		out[end]++
		return
	}

	unvisited := st.Invert(g.GetTotalCells())
	for n := range g.GetNeighborMask(end).Intersect(unvisited).AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() {
			if pruned, _ := p.ShouldPruneAfterVisit(n, newUnvisited); pruned {
				continue
			}
		}
		descendRow(ctx, g, p, st.Visit(n), n, out)
	}
}

// Matrix is the table of N(s,t) over all board cell pairs. It is opaque;
// access goes through At and Total. The filled matrix is deterministic and
// independent of the worker count; a terminated ctx yields a partial matrix
// (zero rows for unclaimed representatives) without panicking.
type Matrix struct {
	data []uint64 // row-major: data[s*cells + t], cells = size²
	size int
}

// Compute counts the matrix: rows are taken for the canonical representatives
// of the start orbits (symmetry.GetCanonicalGroups) in parallel through
// workerpool.Pool.Run, then all remaining cells are completed sequentially by
// the formula N(σs,σt) = N(s,t) — an assignment, idempotent because the
// equivariance makes every image of one pair agree. workers ≥ 1 is the caller
// contract (workerpool semantics).
func Compute(ctx context.Context, g *graph.Graph, workers int) *Matrix {
	sym := symmetry.NewSymmetry(g.Size())
	groups := sym.GetCanonicalGroups()

	rows := make([][]uint64, len(groups))
	workerpool.New(workers).Run(ctx, len(groups), func(ctx context.Context, idx iter.Seq[int]) {
		for i := range idx {
			rows[i] = Row(ctx, g, groups[i].Canonical)
		}
	})

	m := newMatrix(g)
	for i, row := range rows {
		if row == nil {
			continue // ctx ended before this group was claimed — its cells stay zero
		}
		m.applyRow(sym, groups[i].Canonical, row)
	}
	return m
}

// At returns the number of directed Hamiltonian paths from s to t; a cell
// index outside [0, size²) panics.
func (m *Matrix) At(s, t int) uint64 {
	cells := m.size * m.size
	if s < 0 || s >= cells || t < 0 || t >= cells {
		panic(fmt.Sprintf("pairs: cell index (%d,%d) out of range for %dx%d board", s, t, m.size, m.size))
	}
	return m.data[s*cells+t]
}

// Total returns the sum of all matrix cells: the directed board total as a
// path set (the same reference numbers as the pipeline, docs/requirements.md).
func (m *Matrix) Total() uint64 {
	var total uint64
	for _, w := range m.data {
		total += w
	}
	return total
}

// newMatrix allocates an empty cells×cells table for g.
func newMatrix(g *graph.Graph) *Matrix {
	cells := g.GetTotalCells()
	return &Matrix{size: g.Size(), data: make([]uint64, cells*cells)}
}

// applyRow writes the row N(rep,·) and its eight D4 images into m. Assigning
// (not adding) is exact: every matrix cell (s,t) is reached through some
// transform of the rep's orbit, and all such routes carry the same value by
// equivariance N(σs,σt) = N(s,t).
func (m *Matrix) applyRow(sym *symmetry.Symmetry, rep int, row []uint64) {
	cells := m.size * m.size
	for t := range symmetry.NumTransforms {
		s := sym.TransformCell(uint8(t), rep) * cells
		for c, w := range row {
			m.data[s+sym.TransformCell(uint8(t), c)] = w
		}
	}
}
