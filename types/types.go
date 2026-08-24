package types

type Result struct {
	TotalPathsFound int
	CachedPaths     int
}

func (r *Result) Add(other Result) {
	r.TotalPathsFound += other.TotalPathsFound
	r.CachedPaths += other.CachedPaths
}
