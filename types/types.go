package types

import "knighttour/state"

type Result struct {
	TotalPathsFound int
	Pruned          int
}

func (r *Result) Add(other Result) {
	r.TotalPathsFound += other.TotalPathsFound
	r.Pruned += other.Pruned
}

type Subtask struct {
	State           state.State
	Start           int
	End             int
	Depth           int
	SymmetriesCount int
}
