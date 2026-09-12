package types

import "knighttour/pruner"

// Result carries per-subtask statistics collected by the hot DFS and reported
// to monitoring once the subtask completes. Prune counters are split by the
// reason returned from ShouldPruneAfterVisit; Pruned is their sum.
type Result struct {
	TotalPathsFound int // reversal count-DFS only: completions of the subtask;
	// class mode leaves it zero (counting publishes weighted paths via ReportPathsFound)

	CacheWrites int // accumulator emissions (sink.Add, incl. merges into existing keys)

	CacheHits   int // reversal mode only: task-cache lookups answered at the stop level
	CacheMisses int // reversal mode only: task-cache lookups with no entry (h == 0)

	Pruned          int // branches cut by ShouldPruneAfterVisit (sum of the breakdown below)
	PrunedDeadEnd   int // local dead-end: isolated cell / lone unreachable cell
	PrunedNoCont    int // last has no unvisited neighbor (no continuation)
	PrunedDisconn   int // G[unvisited] is not connected
	PrunedEndpoints int // degree-1 endpoint heuristic violated

	PrunedArticulation int // L2: articulation point of the state graph (shapecount DP only)
	PrunedForcedChain  int // L2: contradictory forced chains (shapecount DP only)

	DPStates int // shapecount only: evaluated DP states (memo misses), the "DP nodes" metric

	TailLookups int // shapecount only: persistent tail memo probes (plan 03)
	TailHits    int // shapecount only: tail memo hits (hit rate = TailHits/TailLookups)

	FilteredShapes int // shapecount only: shapes killed by the pre-DP feasibility
	// filter (plan 02); tracked apart from Pruned* so historical pruning metrics
	// stay comparable.
}

// Add folds other into r field by field. other is passed by pointer: Result
// is a wide counter block and copying it per merge would be pure overhead.
func (r *Result) Add(other *Result) {
	r.TotalPathsFound += other.TotalPathsFound
	r.CacheWrites += other.CacheWrites
	r.CacheHits += other.CacheHits
	r.CacheMisses += other.CacheMisses
	r.Pruned += other.Pruned
	r.PrunedDeadEnd += other.PrunedDeadEnd
	r.PrunedNoCont += other.PrunedNoCont
	r.PrunedDisconn += other.PrunedDisconn
	r.PrunedEndpoints += other.PrunedEndpoints
	r.PrunedArticulation += other.PrunedArticulation
	r.PrunedForcedChain += other.PrunedForcedChain
	r.DPStates += other.DPStates
	r.TailLookups += other.TailLookups
	r.TailHits += other.TailHits
	r.FilteredShapes += other.FilteredShapes
}

// CountPrune records one pruned branch under the reason returned by the pruner.
// Hot path: exactly one counter update; NoReason is ignored defensively (it
// means "not pruned"). The aggregate Pruned field is refreshed by Finalize.
func (r *Result) CountPrune(reason pruner.Reason) {
	switch reason {
	case pruner.DeadEnd:
		r.PrunedDeadEnd++
	case pruner.NoContinuation:
		r.PrunedNoCont++
	case pruner.Disconnected:
		r.PrunedDisconn++
	case pruner.Endpoints:
		r.PrunedEndpoints++
	case pruner.Articulation:
		r.PrunedArticulation++
	case pruner.ForcedChain:
		r.PrunedForcedChain++
	}
}

// Finalize sets Pruned to the sum of the per-reason counters. Searcher calls it
// once before returning a Result; CountPrune deliberately does not touch the
// aggregate on every branch.
func (r *Result) Finalize() {
	r.Pruned = r.PrunedDeadEnd + r.PrunedNoCont + r.PrunedDisconn + r.PrunedEndpoints +
		r.PrunedArticulation + r.PrunedForcedChain
}
