package pairs

import (
	"context"
	"math/bits"
	"time"

	"knighttour/graph"
	"knighttour/pruner"
	"knighttour/state"
)

// censusHooks parameterizes the instrumented descent of rowCensus (plan 29):
// the checkpoint schedule and the mark sink. tickNodes is the work period in
// visited nodes at which ctx cancellation and the wall-clock mark are
// evaluated; it is rounded up to a power of two (0 is treated as 1). interval
// is the minimum elapsed time between two time-kind marks; emit receives the
// elapsed time since the descent start and the current counters and must be
// cheap — it runs on the descent path, where marks fire only on power-of-two
// node counts and on ticks throttled by interval (nil disables marks).
type censusHooks struct {
	emit      func(elapsed time.Duration, nodes, leaves uint64)
	tickNodes uint64
	interval  time.Duration
}

// rowCensus is the instrumented twin of Row (plan 29): the same anchored
// descent, so with a live ctx it returns the bit-identical histogram
// out[t] = N(start,t), additionally counting visited nodes and completed
// completions (leaves) and marking progress through hooks.emit. The counters
// are observers: they change neither the visit order nor the cutoffs; keep the
// descent in sync with descendRow — TestRowCensusMatchesRow catches drift.
// Deterministic: the same board and start reproduce the counters bit-for-bit
// between runs. Unlike Row, ctx is checked only on work ticks (every tickNodes
// nodes), not per node, keeping clock and cancellation checks off the hot path;
// a stopped descent yields a partial histogram without panicking — Row's
// contract at a coarser granularity.
func rowCensus(ctx context.Context, g *graph.Graph, start int, hooks censusHooks) (hist []uint64, nodes, leaves uint64) {
	if hooks.tickNodes < 2 {
		hooks.tickNodes = 1
	} else if hooks.tickNodes&(hooks.tickNodes-1) != 0 {
		// Round up to a power of two so the tick test stays a single mask compare.
		hooks.tickNodes = 1 << bits.Len64(hooks.tickNodes-1)
	}
	r := censusRun{ctx: ctx, g: g, p: pruner.New(g), hooks: hooks,
		t0: time.Now(), nextPow: 1, tickMask: hooks.tickNodes - 1}
	r.hist = make([]uint64, g.GetTotalCells())
	r.descend(state.NewState(start), start)
	return r.hist, r.nodes, r.leaves
}

// censusRun carries the mutable state of one rowCensus descent: the counters
// and the checkpoint schedule. It is allocated once per descent; the hot path
// only increments fields through the receiver pointer — descend itself never
// allocates.
type censusRun struct {
	hooks    censusHooks
	t0       time.Time
	ctx      context.Context
	g        *graph.Graph
	p        *pruner.Pruner
	hist     []uint64
	nodes    uint64
	leaves   uint64
	nextPow  uint64
	tickMask uint64
	lastEmit time.Duration
}

// descend is the anchored DFS mirroring descendRow: a full-board leaf adds one
// to the slot of its last cell and counts as one census leaf; candidates and
// cutoffs are identical so the histograms agree end by end. The return value
// propagates termination: once a checkpoint reports ctx over, every pending
// frame returns at once — otherwise the parent loops would still walk the rest
// of the tree in empty calls and the stop latency would not be one tick. That
// extra branch per child is the only hot-path difference from descendRow.
func (r *censusRun) descend(st state.State, end int) bool {
	r.nodes++
	if !r.checkpoint() {
		return false // ctx ended at a tick — unwind with a partial histogram
	}

	if st.CountBits() == r.g.GetTotalCells() {
		r.leaves++
		r.hist[end]++
		return true
	}

	unvisited := st.Invert(r.g.GetTotalCells())
	for n := range r.g.GetNeighborMask(end).Intersect(unvisited).AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() {
			if pruned, _ := r.p.ShouldPruneAfterVisit(n, newUnvisited); pruned {
				continue
			}
		}
		if !r.descend(st.Visit(n), n) {
			return false
		}
	}
	return true
}

// checkpoint is the per-node bookkeeping: a deterministic mark on every
// power-of-two node count, and on work ticks the ctx check plus at most one
// time-kind mark per hooks.interval. It returns false when ctx has ended; the
// ctx check lives on ticks by design so the hot path stays clock-free.
func (r *censusRun) checkpoint() bool {
	if r.nodes == r.nextPow {
		r.nextPow <<= 1
		r.mark(time.Since(r.t0))
	}
	if r.nodes&r.tickMask != 0 {
		return true
	}
	if r.ctx.Err() != nil {
		return false
	}
	if el := time.Since(r.t0); el >= r.lastEmit+r.hooks.interval {
		r.lastEmit = el
		r.mark(el)
	}
	return true
}

// mark hands the current counters to hooks.emit when a sink is present.
func (r *censusRun) mark(elapsed time.Duration) {
	if r.hooks.emit != nil {
		r.hooks.emit(elapsed, r.nodes, r.leaves)
	}
}
