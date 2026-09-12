package searcher

import (
	"context"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/path"
	"knighttour/pruner"
	"knighttour/state"
	"knighttour/symmetry"
	"knighttour/types"
)

type Searcher struct {
	graph  *graph.Graph
	sym    *symmetry.Symmetry
	pruner *pruner.Pruner
}

func NewSearcher(g *graph.Graph, sym *symmetry.Symmetry) *Searcher {
	return &Searcher{
		graph:  g,
		sym:    sym,
		pruner: pruner.New(g),
	}
}

// GenerateRoots is phase A of generation (specs/counter.md): DFS from a
// canonical start down to depth, emitting each prefix as its D4-canonical
// (state, end) pair with the group's orbit weight. The sink is caller-owned
// (cache.LocalSink buffers and batches into the shared accumulator).
// Statistics: CacheWrites counts emitted prefixes.
func (s *Searcher) GenerateRoots(ctx context.Context, sink *cache.LocalSink, start int, orbitSize uint64, depth int) (result types.Result) {
	if s.graph.SholdSkip(start) {
		return result
	}
	s.dfsGen(ctx, state.NewState(start), start, depth, orbitSize, sink, &result)
	result.Finalize()
	return result
}

// ExtendToClasses is phase B of the class-mode pipeline (specs/shapecount.md):
// it extends an intermediate entry to the target depth like a prefix run, but
// on each leaf emits complement shape classes into sink instead of a prefix
// record. For leaf (T,t) with U = full \ T and every u ∈ N(t) ∩ U:
// M[class(U,u)] += weight — the exact regrouping of Σ W·f over the reversal
// identity, so Σ_C h(C)·M(C) equals the plain count. An entry already at the
// target depth (precomputeDepth ≤ base depth) emits immediately from itself.
// Statistics reuse the cache-shaped fields: CacheWrites counts emitted class
// contributions.
func (s *Searcher) ExtendToClasses(ctx context.Context, sink *cache.LocalSink, p path.Path, weight uint64, depth int) (result types.Result) {
	s.dfsEmit(ctx, p.State(), p.End(), depth, weight, sink, &result)
	result.Finalize()
	return result
}

// dfsGen is the phase-A descent: it walks prefixes down to depth and emits each
// prefix once, as its own D4-canonical placement (state, end) carrying the start
// group's orbit weight. The visited set is the payload, so the depth check comes
// before unvisited/cand are computed — a leaf needs neither.
func (s *Searcher) dfsGen(ctx context.Context, st state.State, end, depth int, weight uint64, sink *cache.LocalSink, res *types.Result) {
	if ctx.Err() != nil {
		return
	}

	if st.CountBits() >= depth {
		sink.Add(s.sym.Canonicalize(st, end), weight)
		res.CacheWrites++
		return
	}

	unvisited := st.Invert(s.graph.GetTotalCells())
	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

	for n := range cand.AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() {
			if pruned, reason := s.pruner.ShouldPruneAfterVisit(n, newUnvisited); pruned {
				res.CountPrune(reason)
				continue
			}
		}
		s.dfsGen(ctx, st.Visit(n), n, depth, weight, sink, res)
	}
}

// dfsEmit is the phase-B descent: same traversal and pruning as dfsGen, but a
// leaf contributes its complement instead of itself. For leaf (T,t) with
// U = full \ T it normalizes U once (PrepareShape) and emits one class key per
// u ∈ N(t) ∩ U, all with the same incoming weight — hence up to deg(t) emissions
// per leaf, and zero when cand is empty (such a tail closes no tour, so its
// contribution to M is legitimately nil). unvisited/cand are computed before the
// depth check because the base case needs both.
func (s *Searcher) dfsEmit(ctx context.Context, st state.State, end, depth int, weight uint64, sink *cache.LocalSink, res *types.Result) {
	if ctx.Err() != nil {
		return
	}

	unvisited := st.Invert(s.graph.GetTotalCells())
	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

	if st.CountBits() >= depth {
		var sc symmetry.ShapeCtx
		s.sym.PrepareShape(unvisited, &sc)
		for u := range cand.AllVisited() {
			sink.Add(s.sym.KeyFromPrepared(&sc, int(u)), weight)
			res.CacheWrites++
		}
		return
	}

	for n := range cand.AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() {
			if pruned, reason := s.pruner.ShouldPruneAfterVisit(n, newUnvisited); pruned {
				res.CountPrune(reason)
				continue
			}
		}
		s.dfsEmit(ctx, st.Visit(n), n, depth, weight, sink, res)
	}
}
