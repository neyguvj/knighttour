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

// GenerateTasks is phase A of reversal-mode generation (specs/counter.md):
// the same descent as dfsGen, but each leaf prefix is written directly into
// the shared task-cache as its D4-canonical placement with the group's orbit
// weight (no LocalSink: cache.Set is the write path, ADR-011). SholdSkip
// starts emit nothing. Statistics: CacheWrites counts task records written.
func (s *Searcher) GenerateTasks(ctx context.Context, c *cache.Cache, start int, orbitSize uint64, depth int) (result types.Result) {
	if s.graph.SholdSkip(start) {
		return result
	}
	s.dfsTask(ctx, state.NewState(start), start, depth, orbitSize, c, &result)
	result.Finalize()
	return result
}

// ExtendTask is phase B of reversal-mode generation: it continues the task
// descent from an already canonical entry p with its aggregated weight down
// to the target depth. An entry at or beyond the depth (degenerate split,
// precomputeDepth ≤ base) writes itself as-is — descending further would
// lose its record; below the threshold leaves are written canonicalized like
// in GenerateTasks. No SholdSkip check: roots were filtered by phase A.
func (s *Searcher) ExtendTask(ctx context.Context, c *cache.Cache, p path.Path, weight uint64, depth int) (result types.Result) {
	if p.State().CountBits() >= depth {
		c.Set(p, weight)
		result.CacheWrites++
		result.Finalize()
		return result
	}
	s.dfsTask(ctx, p.State(), p.End(), depth, weight, c, &result)
	result.Finalize()
	return result
}

// CountPathsWithCacheReversal counts the full completions of p with early
// stop at level totalCells−d (specs/searcher.md): at that level the remainder
// U = full \ T is answered by Σ W(canon(U,u))/orbitSize over u ∈ N(t) ∩ U from
// c instead of descending to a full board. c == nil or 2d > totalCells → full
// descent without any cache access (the reversal duality is unreachable).
// Statistics: TotalPathsFound, CacheHits/CacheMisses per lookup, pruning by
// reason.
func (s *Searcher) CountPathsWithCacheReversal(ctx context.Context, p path.Path, c *cache.Cache, d int) (result types.Result) {
	stopLevel := -1 // disabled sentinel: full descent
	if c != nil && 2*d <= s.graph.GetTotalCells() {
		stopLevel = s.graph.GetTotalCells() - d
	}
	result.TotalPathsFound = s.dfsCount(ctx, p.State(), p.End(), stopLevel, c, &result)
	result.Finalize()
	return result
}

// dfsTask is the task-cache descent of reversal-mode generation: same walk
// and pruning as dfsGen, but the leaf writes its canonical placement into the
// shared cache. Duplicated rather than callback-unified with dfsGen for the
// hot-path reason documented in specs/searcher.md.
func (s *Searcher) dfsTask(ctx context.Context, st state.State, end, depth int, weight uint64, c *cache.Cache, res *types.Result) {
	if ctx.Err() != nil {
		return
	}

	if st.CountBits() >= depth {
		c.Set(s.sym.Canonicalize(st, end), weight)
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
		s.dfsTask(ctx, st.Visit(n), n, depth, weight, c, res)
	}
}

// dfsCount is the reversal count-DFS: full descent (a full board counts 1)
// unless stopLevel ≥ 0 and the visit count hits it exactly, where Completions
// answers through the task cache. Steps grow bits one at a time, so == suffices.
func (s *Searcher) dfsCount(ctx context.Context, st state.State, end, stopLevel int, c *cache.Cache, res *types.Result) int {
	if ctx.Err() != nil {
		return 0
	}

	total := s.graph.GetTotalCells()
	bits := st.CountBits()
	if bits >= total {
		return 1
	}

	unvisited := st.Invert(total)
	if stopLevel >= 0 && bits == stopLevel {
		return s.completions(unvisited, end, c, res)
	}

	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

	found := 0
	for n := range cand.AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() {
			if pruned, reason := s.pruner.ShouldPruneAfterVisit(n, newUnvisited); pruned {
				res.CountPrune(reason)
				continue
			}
		}
		found += s.dfsCount(ctx, st.Visit(n), n, stopLevel, c, res)
	}
	return found
}

// completions answers f(T,end) at the stop level for U = unvisited: every
// completion of (T,t) reverses to a suffix covering exactly U and ending at a
// neighbor u of t, so f(T,t) = Σ_{u∈U, u~t} h(U,u). The task-cache weight of
// canon(U,u) is the sum of orbit sizes over all depth-d prefixes in that key's
// fiber — W = orbitSize·h(U,u) — hence the exact division (specs/searcher.md,
// ADR-011). Missing entries contribute 0 (no such prefix ⇒ h == 0); every
// lookup is attributed to res without atomics (one Result per worker).
func (s *Searcher) completions(unvisited state.State, end int, c *cache.Cache, res *types.Result) int {
	cand := s.graph.GetNeighborMask(end).Intersect(unvisited)
	states := s.sym.TransformStates(unvisited)

	found := 0
	for u := range cand.AllVisited() {
		canonical, orbitSize := s.sym.CanonicalFromStates(states, int(u))
		weight, ok := c.Get(canonical)
		if !ok {
			res.CacheMisses++
			continue
		}
		res.CacheHits++
		found += int(weight / uint64(orbitSize))
	}
	return found
}
