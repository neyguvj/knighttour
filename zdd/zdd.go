package zdd

import (
	"fmt"
	"iter"
	"slices"
)

// Diagram is a built, reduced and zero-suppressed decision diagram of the
// Hamiltonian path or cycle edge-set family of one graph. It is immutable
// after Build and safe for concurrent reads.
type Diagram struct {
	arena     []znode // node storage; index 0 is the idNil sentinel
	order     []int   // position → original edge index (needed by All)
	root      uint32  // root node id (idZero for an empty family)
	levelPeak int     // max non-terminal nodes sharing one variable level

	// Memo-table census at build end (plan 24): internal to the package,
	// published only by BenchmarkZDDCensus. Zero for a skipped build.
	memoEntries int
	zeroEntries int
	zeroHits    int
}

// Build constructs the reduced ZDD of the Hamiltonian edge-set family fam of
// the graph with nVerts vertices and the given edge list. order is a
// permutation of the edge indices selecting the variable order; nil means row
// order (the order the edges are listed in). Invalid input — an unknown fam,
// nVerts outside [1, 64], an endpoint outside [0, nVerts), a loop, a duplicate
// edge, or an order that is not a permutation — returns an error. An isolated
// vertex makes the family empty: Build then returns a diagram reduced to the
// zero terminal (Count 0). Exceeding the soft byte ceiling stops the build
// with an OverCapError (errors.Is ErrOverCap) instead of exhausting memory.
//
// The build is single-threaded and deterministic; see the package doc.
func Build(nVerts int, edges [][2]int, fam Family, order []int) (*Diagram, error) {
	if !fam.valid() {
		return nil, fmt.Errorf("zdd: unknown family %d", int(fam))
	}
	f, err := newFrontier(nVerts, edges, order)
	if err != nil {
		return nil, err
	}
	b := &builder{f: *f, a: *newArena(), uniq: *newUniqueTable(), memo: *newMemoTables(f.m), fam: fam}
	root := idZero
	if !f.empty && (fam != Cycles || !parityEmptyCycles(nVerts, edges)) {
		root = b.build()
		if b.err != nil {
			return nil, b.err
		}
	}
	return &Diagram{
		arena:       b.a.nodes,
		order:       b.f.order,
		root:        root,
		levelPeak:   peakLevel(&b.a, f.m),
		memoEntries: b.memo.memoEntries,
		zeroEntries: b.memo.zeroEntries,
		zeroHits:    b.memo.zeroHits,
	}, nil
}

// LevelPeak returns the maximum number of non-terminal nodes on one diagram
// level — the measured node width of the widest cut (0 for an empty family).
func (d *Diagram) LevelPeak() int { return d.levelPeak }

// peakLevel returns the largest group of arena nodes testing the same edge
// position. A node's variable is its level in [0, m), so counting into one
// array of m counters is exact: no per-node copy and no sort, which keeps this
// pass from inflating the peak RSS that the census reports.
func peakLevel(a *arena, m int) int {
	counts := make([]int, m)
	for _, n := range a.nodes[numTerminals:] {
		counts[n.variable]++
	}
	peak := 0
	for _, c := range counts {
		peak = max(peak, c)
	}
	return peak
}

// Count returns the number of edge sets in the family. Each undirected tour
// is one set; directed tours equal 2×Count (plan 23 convention).
func (d *Diagram) Count() uint64 {
	return d.arena[d.root].weight
}

// LiveNodes returns the number of allocated diagram nodes, terminals included
// (the arena sentinel excluded). After a successful build it equals both the
// final and the peak node count.
func (d *Diagram) LiveNodes() int {
	return len(d.arena) - 1
}

// All iterates over the family's edge sets as ascending slices of original
// edge indices. It enumerates exponentially many sets — tests and small graphs
// only. Iteration order is deterministic.
func (d *Diagram) All() iter.Seq[[]int] {
	return func(yield func([]int) bool) {
		d.walk(d.root, nil, yield)
	}
}

// walk enumerates the family below node id depth-first (skip branch first),
// accumulating taken positions in path; it reports false when yield stopped
// the iteration.
func (d *Diagram) walk(id uint32, path []int, yield func([]int) bool) bool {
	switch id {
	case idZero:
		return true
	case idOne:
		set := make([]int, len(path))
		for k, p := range path {
			set[k] = d.order[p]
		}
		slices.Sort(set)
		return yield(set)
	}
	if !d.walk(d.arena[id].low, path, yield) {
		return false
	}
	path = append(path, int(d.arena[id].variable))
	return d.walk(d.arena[id].high, path, yield)
}
