package types

import "knighttour/pruner"

// Result carries per-subtask statistics collected by the hot DFS and reported
// to monitoring once the subtask completes. Prune counters are split by the
// reason returned from ShouldPruneAfterVisit; Pruned is their sum.
type Result struct {
	TotalPathsFound int // reversal count-DFS only: completions of the subtask;
	// generation leaves it zero (counting publishes weighted paths via ReportPathsFound)

	CacheWrites int // weight-table writes (Set, incl. merges into existing keys)

	CacheHits   int // reversal count-DFS only: task-cache lookups answered at the stop level
	CacheMisses int // reversal count-DFS only: task-cache lookups with no entry (h == 0)

	Pruned          int // branches cut by ShouldPruneAfterVisit (sum of the breakdown below)
	PrunedDeadEnd   int // local dead-end: isolated cell / lone unreachable cell
	PrunedNoCont    int // last has no unvisited neighbor (no continuation)
	PrunedDisconn   int // G[unvisited] is not connected
	PrunedEndpoints int // degree-1 endpoint heuristic violated
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
	}
}

// Finalize sets Pruned to the sum of the per-reason counters. Searcher calls it
// once before returning a Result; CountPrune deliberately does not touch the
// aggregate on every branch.
func (r *Result) Finalize() {
	r.Pruned = r.PrunedDeadEnd + r.PrunedNoCont + r.PrunedDisconn + r.PrunedEndpoints
}
