package types

type Result struct {
	TotalPathsFound int // number of full paths found in the subtree
	CacheWrites     int // number of cache.Set calls (writes, incl. updates of existing keys)
	Pruned          int // branches cut by ShouldPruneAfterVisit
}

func (r *Result) Add(other Result) {
	r.TotalPathsFound += other.TotalPathsFound
	r.CacheWrites += other.CacheWrites
	r.Pruned += other.Pruned
}
