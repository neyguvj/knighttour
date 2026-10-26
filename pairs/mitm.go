package pairs

import (
	"context"

	"knighttour/graph"
	"knighttour/pruner"
	"knighttour/state"
)

// mitmSide is one enumerated side of the meet-in-the-middle cut: partial-path
// weights keyed by (mask, end), grouped by mask so the glue can probe a mask
// complement in one lookup. walks counts the enumerated partial paths with
// multiplicity (the census column of specs/benchmarks.md).
type mitmSide struct {
	byMask map[state.State]map[uint8]uint64
	walks  int64
}

// mitmCensus carries the probe counters published by BenchmarkPairsProbe:
// walksP/walksT are the enumerated partial paths of the prefix and suffix
// sides (the latter summed over all ends), entries is the number of (mask,end)
// records in the prefix map.
type mitmCensus struct {
	walksP  int64
	walksT  int64
	entries int
}

// mitmRow is the independent meet-in-the-middle probe of the same matrix row
// as Row (specs/pairs.md): prefixes of length m from start are collected into
// a (mask,end)→weight map, then for every end cell a table of reversed
// suffixes of depth totalCells−m is built and glued by complementary mask and
// end adjacency. The precondition is 0 < m < g.GetTotalCells(); out of the
// domain the probe returns a zero row. Used by tests and the probe benchmark
// of this package; not part of the public API.
func mitmRow(ctx context.Context, g *graph.Graph, start, m int) []uint64 {
	row, _ := mitmProbe(ctx, g, start, m)
	return row
}

// mitmProbe runs mitmRow's descent and reports the side censuses alongside.
// The prefix side is cut by the pruner (safe by the same necessary condition
// as Row); the suffix side is enumerated without cutoffs — cross-side pruning
// is out of the probe's design (plan 28 "Корректность").
func mitmProbe(ctx context.Context, g *graph.Graph, start, m int) ([]uint64, mitmCensus) {
	cells := g.GetTotalCells()
	out := make([]uint64, cells)
	if m < 1 || m >= cells {
		return out, mitmCensus{}
	}

	full := state.State(0).Invert(cells)
	prefixes := mitmWalk(ctx, g, pruner.New(g), start, m)
	cen := mitmCensus{walksP: prefixes.walks, entries: prefixes.records()}

	for t := range cells {
		if ctx.Err() != nil {
			return out, cen
		}
		suffixes := mitmWalk(ctx, g, nil, t, cells-m)
		cen.walksT += suffixes.walks
		out[t] = mitmGlue(g, prefixes, suffixes, full)
	}
	return out, cen
}

// mitmWalk enumerates every simple path of exactly depth cells starting at
// anchor (the anchor counts as one) into a side table. A nil pruner disables
// the after-visit cutoffs; ctx termination stops the walk early and yields
// the partial table without panicking.
func mitmWalk(ctx context.Context, g *graph.Graph, p *pruner.Pruner, anchor, depth int) *mitmSide {
	side := &mitmSide{byMask: make(map[state.State]map[uint8]uint64)}
	mitmDescend(ctx, g, p, state.Bit(anchor), anchor, depth, side)
	return side
}

// mitmDescend grows the partial paths one cell at a time and records each
// path of the target depth under its (mask, end).
func mitmDescend(ctx context.Context, g *graph.Graph, p *pruner.Pruner, st state.State, end, depth int, side *mitmSide) {
	if ctx.Err() != nil {
		return
	}

	if st.CountBits() == depth {
		side.record(st, end)
		return
	}

	unvisited := st.Invert(g.GetTotalCells())
	for n := range g.GetNeighborMask(end).Intersect(unvisited).AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if p != nil && !newUnvisited.IsEmpty() {
			if pruned, _ := p.ShouldPruneAfterVisit(n, newUnvisited); pruned {
				continue
			}
		}
		mitmDescend(ctx, g, p, st.Visit(n), n, depth, side)
	}
}

// mitmGlue counts the paths assembled from one prefix and one suffix: a pair
// of records glues when its masks partition the board and the ends are
// adjacent. Every directed path crosses the cut in exactly one way (prefix
// x_0..x_{m−1}, reversed suffix x_{N−1}..x_m), so the sum is exact.
func mitmGlue(g *graph.Graph, prefixes, suffixes *mitmSide, full state.State) uint64 {
	var total uint64
	for mask, ends := range prefixes.byMask {
		complement := full.AndNot(mask)
		sufEnds, found := suffixes.byMask[complement]
		if !found {
			continue
		}
		for end, wp := range ends {
			for v := range g.GetNeighborMask(int(end)).Intersect(complement).AllVisited() {
				if wt, ok := sufEnds[uint8(v)]; ok {
					total += wp * wt
				}
			}
		}
	}
	return total
}

// record adds one enumerated path to the side table.
func (side *mitmSide) record(mask state.State, end int) {
	side.walks++
	ends := side.byMask[mask]
	if ends == nil {
		ends = make(map[uint8]uint64)
		side.byMask[mask] = ends
	}
	ends[uint8(end)]++
}

// records returns the number of distinct (mask,end) keys of the side.
func (side *mitmSide) records() int {
	n := 0
	for _, ends := range side.byMask {
		n += len(ends)
	}
	return n
}
