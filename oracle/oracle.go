// Package oracle memoizes h(mask,end): the number of knight paths that cover
// exactly mask and end at end. Keys are canonical under D4 combined with
// translations, because h depends only on the induced subgraph of mask with a
// marked endpoint (knight adjacency is translation invariant), not on where
// the shape sits on the board. See specs/oracle.md.
package oracle

import (
	"sync"
	"sync/atomic"

	"knighttour/graph"
	"knighttour/pruner"
	"knighttour/state"
	"knighttour/symmetry"
)

const (
	numShards = 16
	maxCells  = 64
)

// key is the canonical representative of a (mask,end) class: bbox pinned to
// the board origin plus the relative end position.
type key struct {
	shape state.State
	end   uint8
}

type shard struct {
	data map[key]uint64
	sync.RWMutex
}

// Oracle is a concurrency-safe memo table for h(mask,end). Misses are computed
// outside the lock and inserted under it; concurrent misses on one key may
// duplicate work but never corrupt values (computation is deterministic).
type Oracle struct {
	graph       *graph.Graph
	pruner      *pruner.Pruner
	shards      [numShards]shard
	size        int
	lookups     atomic.Uint64
	computes    atomic.Uint64
	zeroClasses atomic.Uint64
	perms       [symmetry.NumTransforms][maxCells]uint8
	rows        [maxCells]uint8
	cols        [maxCells]uint8
}

func New(g *graph.Graph) *Oracle {
	o := &Oracle{graph: g, pruner: pruner.New(g), size: g.Size()}

	total := g.GetTotalCells()
	closures := symmetry.GetSymmetries(o.size)
	for t, f := range closures {
		for pos := range total {
			nx, ny := f(pos/o.size, pos%o.size, o.size)
			o.perms[t][pos] = uint8(nx*o.size + ny)
		}
	}
	for pos := range maxCells {
		o.rows[pos] = uint8(pos / o.size)
		o.cols[pos] = uint8(pos % o.size)
	}

	for i := range o.shards {
		o.shards[i].data = make(map[key]uint64)
	}
	return o
}

// ShapeCtx holds per-mask normalization work (one Prepare call) so a node at
// the reversal level can query several ends of the same mask without redoing
// the 8 orientation transforms. Zero value is ready for Prepare.
type ShapeCtx struct {
	shapes [symmetry.NumTransforms]state.State
	dxs    [symmetry.NumTransforms]uint8
	dys    [symmetry.NumTransforms]uint8
}

// Prepare fills sc with the normalized shape of mask under every orientation:
// each transformed mask is translated so its bbox starts at (0,0); dxs/dys
// keep that translation for end re-encoding in GetPrepared. The D4 group maps
// axis-aligned bboxes exactly onto axis-aligned bboxes, so all eight offsets
// are derived from the original bbox and each orientation needs a single pass.
func (o *Oracle) Prepare(mask state.State, sc *ShapeCtx) {
	var pos [maxCells]uint8
	n := 0
	minR, minC := byte(255), byte(255)
	var maxR, maxC uint8
	for p := range mask.AllVisited() {
		pos[n] = uint8(p)
		n++
		r, c := o.rows[p], o.cols[p]
		if r < minR {
			minR = r
		}
		if r > maxR {
			maxR = r
		}
		if c < minC {
			minC = c
		}
		if c > maxC {
			maxC = c
		}
	}

	last := byte(o.size - 1)
	// Orientation order matches symmetry.GetSymmetries; entries give the
	// (min row, min col) of that orientation's transformed bbox.
	orR := [symmetry.NumTransforms]uint8{minR, minC, last - maxR, last - maxC, minR, last - maxR, minC, last - maxC}
	orC := [symmetry.NumTransforms]uint8{minC, last - maxR, last - maxC, minR, last - maxC, minC, minR, last - maxR}

	for t := range sc.shapes {
		rOff, cOff := orR[t], orC[t]
		var shape state.State
		for _, p := range pos[:n] {
			tp := o.perms[t][p]
			shape = shape.Visit(int(o.rows[tp]-rOff)*o.size + int(o.cols[tp]-cOff))
		}
		sc.shapes[t], sc.dxs[t], sc.dys[t] = shape, rOff, cOff
	}
}

// normEnd re-encodes end under orientation t using the stored bbox offset.
func (o *Oracle) normEnd(sc *ShapeCtx, t, end int) uint8 {
	p := o.perms[t][end]
	r, c := o.rows[p]-sc.dxs[t], o.cols[p]-sc.dys[t]
	return uint8(int(r)*o.size + int(c))
}

// keyOf returns the canonical key for (already prepared mask, end): the
// lexicographic minimum over orientations; ties on shape are broken by end.
func (o *Oracle) keyOf(sc *ShapeCtx, end int) key {
	best, bestEnd := 0, o.normEnd(sc, 0, end)
	for t := 1; t < len(sc.shapes); t++ {
		e := o.normEnd(sc, t, end)
		if sc.shapes[t] < sc.shapes[best] || (sc.shapes[t] == sc.shapes[best] && e < bestEnd) {
			best, bestEnd = t, e
		}
	}
	return key{shape: sc.shapes[best], end: bestEnd}
}

func shardIndex(k key) int {
	h := uint64(k.shape)*0x9E3779B97F4A7C15 + uint64(k.end)*0xC2B2AE3D27D4EB4F
	return int(h >> (64 - 4)) // numShards = 16
}

// Get returns the true h(mask,end) for end ∈ mask; 0 when end ∉ mask.
// A zero value is a valid memo entry, not a miss.
func (o *Oracle) Get(mask state.State, end int) uint64 {
	if !mask.IsVisited(end) {
		return 0
	}
	var sc ShapeCtx
	o.Prepare(mask, &sc)
	return o.GetPrepared(&sc, end)
}

// GetPrepared is Get for a mask already passed to Prepare: the orientation
// work is amortized across ends of the same mask. The caller must guarantee
// end ∈ mask (the searcher does).
func (o *Oracle) GetPrepared(sc *ShapeCtx, end int) uint64 {
	k := o.keyOf(sc, end)
	o.lookups.Add(1)

	sh := &o.shards[shardIndex(k)]
	sh.RLock()
	v, ok := sh.data[k]
	sh.RUnlock()
	if ok {
		return v
	}

	o.computes.Add(1)
	h := o.computeH(k.shape, int(k.end))

	sh.Lock()
	defer sh.Unlock()
	if v, ok := sh.data[k]; ok { // double-check: another writer won the race.
		return v
	}
	sh.data[k] = h
	if h == 0 {
		o.zeroClasses.Add(1) // useless class: no routes found inside it.
	}
	return h
}

// computeH counts knight paths starting at end that visit every cell of shape
// exactly once (the reversal bijection makes this equal to h(shape,end)).
func (o *Oracle) computeH(shape state.State, end int) uint64 {
	type memoKey struct {
		todo state.State
		cur  uint8
	}
	size := shape.CountBits()
	memoCap := 1 << max(min(size, 12), 4)
	memo := make(map[memoKey]uint64, memoCap)

	var walk func(cur int, todo state.State) uint64
	walk = func(cur int, todo state.State) uint64 {
		if todo == 0 {
			return 1
		}
		mk := memoKey{todo: todo, cur: uint8(cur)}
		if v, ok := memo[mk]; ok {
			return v
		}
		// Pruning is sound here: ShouldPruneAfterVisit only cuts states with no
		// completion, i.e. a true zero. It runs on normalized shape coordinates;
		// knight adjacency is translation invariant and board-edge clipping only
		// drops off-board cells (never in todo), so G[todo] is seen exactly.
		if pruned, _ := o.pruner.ShouldPruneAfterVisit(cur, todo); pruned {
			memo[mk] = 0
			return 0
		}
		var total uint64
		for n := range o.graph.GetNeighborMask(cur).Intersect(todo).AllVisited() {
			total += walk(n, todo.Unvisit(n))
		}
		memo[mk] = total
		return total
	}

	return walk(end, shape.Unvisit(end))
}

// Stats reports hot-path metrics: total lookups, recomputations (misses that
// triggered computeH), stored classes and zero-valued ("useless") classes —
// those where no route was found. classes/lookups ratios expose the effective
// translation compression of the workload; zeros/classes is the useless share.
func (o *Oracle) Stats() (lookups, computes, classes, zeros uint64) {
	lookups = o.lookups.Load()
	computes = o.computes.Load()
	zeros = o.zeroClasses.Load()
	for i := range o.shards {
		o.shards[i].RLock()
		classes += uint64(len(o.shards[i].data))
		o.shards[i].RUnlock()
	}
	return lookups, computes, classes, zeros
}
