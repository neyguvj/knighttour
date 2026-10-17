package counter

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"math/bits"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"testing"
	"time"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/monitoring"
	"knighttour/pruner"
	"knighttour/state"
	"knighttour/symmetry"
	"knighttour/types"

	"github.com/stretchr/testify/assert"
)

// State-reuse census (plan 22): how often the count-DFS states (visited mask,
// current cell) repeat across class tasks at levels d+1..d+J below the cut —
// the remainder the counting-phase memoization does not cover. replicaCount is
// a test-side copy of searcher.dfsCount over exported APIs only; editing
// dfsCount requires mirroring it here, and the 5×5 pin test catches any
// divergence (specs/benchmarks.md «Ценз переиспользования состояний»).

const (
	// memoCensusBloomHashes is the Bloom filter's hash count (plan 22 budget).
	memoCensusBloomHashes = 7
	// memoCensusFilterBitsPerTask sizes the filter to one sampled task: with a
	// producer half of |S|/2 it keeps the design fill near the plan's budget.
	memoCensusFilterBitsPerTask = 1 << 17
	// memoCensusMaxFilterBits caps the filter at the plan-22 allocation budget.
	memoCensusMaxFilterBits = 1 << 33
	// memoCensusTaskBudget is the per-task insurance in the spirit of
	// censusTaskBudget: an aborted task keeps its partial visits (a
	// conservative undercount) and shows up in aborted/op.
	memoCensusTaskBudget = 10 * time.Second
	// memoWindowModelBytes is the plan-22 layout model for a second-level memo
	// table: 32 B per distinct state (24 B slot at ≤ 0.75 fill, pow-of-2 round).
	memoWindowModelBytes = 32
)

// memoCensusLadder is the |S| saturation ladder of plan 22: growing producer
// pools show how much sampling underestimates cross-task reuse.
var memoCensusLadder = []int{1024, 8192, 65536}

// memoCensusPoint is one capture point: a cut depth and a descent window.
type memoCensusPoint struct {
	depth  int
	window int // 0 → the whole remainder size²−2d; else min(window, remainder)
}

// memoCensusPoints are the plan-22 capture points per board (the -bench filter
// picks which run, ADR-020): 7×7 d10..12 with full remainders, deep 6×6
// windows for the reuse-versus-length curve, cheap 5×5 points, and a small-J
// probe on 8×8 d14 whose full remainder is unaffordable.
var memoCensusPoints = map[int][]memoCensusPoint{
	5: {{depth: 6}, {depth: 10}},
	6: {{depth: 8}, {depth: 14}},
	7: {{depth: 10}, {depth: 11}, {depth: 12}},
	8: {{depth: 14, window: 4}},
}

// memoKeyMode selects the state key of a census run.
type memoKeyMode uint8

const (
	memoKeyRaw       memoKeyMode = iota // raw pair (mask, cur)
	memoKeyCanonical                    // D4 class form CanonicalClass(mask, cur)
)

// String renders the mode as its subtest name segment.
func (m memoKeyMode) String() string {
	if m == memoKeyRaw {
		return "raw"
	}
	return "canon"
}

// memoKeySet is the census key store the replica queries at every window
// state: pass A probes before inserting (self-hits give multiplicity), pass B
// only probes. Implementations allocate nothing per call; fpr reports the
// false-positive probability at the actual fill (0 for an exact store).
type memoKeySet interface {
	probeInsert(key state.State, sub uint8) (hit bool)
	probe(key state.State, sub uint8) bool
	fpr() float64
}

// newMemoTestSet builds a census store by name for table-driven tests.
func newMemoTestSet(kind string) memoKeySet {
	if kind == "bloom" {
		return newMemoBloom(1 << 16)
	}
	return newExactStateSet(1024)
}

// memoBloom is the census Bloom filter: pow-of-2 bits, k independent probes
// from a double hash. Only insertions that missed are counted, so the fill —
// and the published false-positive estimate — track distinct keys rather than
// duplicate re-inserts of pass A.
type memoBloom struct {
	words   []uint64
	bitMask uint64 // m−1 with m = 64·len(words), a power of two
	inserts uint64 // distinct-ish insertions (misses only)
}

// newMemoBloom returns a filter of at least bits bits (rounded up to a
// power-of-two word count, minimum 512 bits).
func newMemoBloom(bitCount int) *memoBloom {
	w := max((bitCount+63)/64, 8)
	n := 1 << bits.Len(uint(w-1))
	return &memoBloom{words: make([]uint64, n), bitMask: uint64(n)*64 - 1}
}

// probeInsert answers whether the key was already present, then sets all its
// bits (memoKeySet producer side); only misses count toward the fill.
func (b *memoBloom) probeInsert(key state.State, sub uint8) bool {
	h1, h2 := memoKeyHash(uint64(key), sub)
	hit := true
	for i := range memoCensusBloomHashes {
		bit := (h1 + uint64(i)*h2) & b.bitMask
		word := &b.words[bit>>6]
		flag := uint64(1) << (bit & 63)
		if *word&flag == 0 {
			hit = false
		}
		*word |= flag
	}
	if !hit {
		b.inserts++
	}
	return hit
}

// probe reports membership without setting bits (memoKeySet reader side).
func (b *memoBloom) probe(key state.State, sub uint8) bool {
	h1, h2 := memoKeyHash(uint64(key), sub)
	for i := range memoCensusBloomHashes {
		bit := (h1 + uint64(i)*h2) & b.bitMask
		if b.words[bit>>6]&(uint64(1)<<(bit&63)) == 0 {
			return false
		}
	}
	return true
}

// fpr returns (1 − e^(−k·n/m))^k for the counted insertions. Counting misses
// only slightly underestimates the fill (a new key seen as present is not
// counted), which keeps the estimate close to the occupancy of distinct keys.
func (b *memoBloom) fpr() float64 {
	if b.inserts == 0 {
		return 0
	}
	m := float64(len(b.words)) * 64
	p := 1 - math.Exp(-float64(memoCensusBloomHashes)*float64(b.inserts)/m)
	return math.Pow(p, memoCensusBloomHashes)
}

// memoKeyHash mixes the key pair into two probe hashes (splitmix64 rounds);
// h2 is odd so the double-hashing probe cycle stays long.
func memoKeyHash(a uint64, sub uint8) (uint64, uint64) {
	x := a ^ splitmix64(uint64(sub)*0x9E3779B97F4A7C15)
	return splitmix64(x), splitmix64(x^0xFF51AFD7ED558CCD) | 1
}

// splitmix64 is the standard finalizer mixing function.
func splitmix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// exactStateSet is the exact open-addressing key set of the calibration
// control: zero false positives, no sampling. A slot is mask+sub; the zero
// mask marks an empty slot because every census state carries bits.
type exactStateSet struct {
	masks []state.State
	subs  []uint8
	count int
}

// newExactStateSet returns a set with room for capacity states before the
// first rehash (rounded up to a power of two, minimum 16 slots).
func newExactStateSet(capacity int) *exactStateSet {
	n := pow2Ceil(max(capacity, 16))
	return &exactStateSet{masks: make([]state.State, n), subs: make([]uint8, n)}
}

// probeInsert reports whether the exact key was stored and inserts it if not
// (memoKeySet producer side); never a false answer.
func (s *exactStateSet) probeInsert(key state.State, sub uint8) bool {
	i := s.slot(key, sub)
	for {
		m := s.masks[i]
		if m == 0 {
			s.masks[i], s.subs[i] = key, sub
			s.count++
			s.grow()
			return false
		}
		if m == key && s.subs[i] == sub {
			return true
		}
		i = (i + 1) & (len(s.masks) - 1)
	}
}

// probe reports exact membership along the probe chain (memoKeySet reader side).
func (s *exactStateSet) probe(key state.State, sub uint8) bool {
	i := s.slot(key, sub)
	for {
		m := s.masks[i]
		if m == 0 {
			return false
		}
		if m == key && s.subs[i] == sub {
			return true
		}
		i = (i + 1) & (len(s.masks) - 1)
	}
}

// fpr is zero: the exact set never answers a miss as a hit.
func (s *exactStateSet) fpr() float64 { return 0 }

// slot returns the initial probe index for the key.
func (s *exactStateSet) slot(key state.State, sub uint8) int {
	h1, h2 := memoKeyHash(uint64(key), sub)
	return int((h1^h2)>>32) & (len(s.masks) - 1)
}

// grow doubles the table past a 0.75 load factor and rehashes live slots.
func (s *exactStateSet) grow() {
	if s.count*4 < len(s.masks)*3 {
		return
	}
	oldMasks, oldSubs := s.masks, s.subs
	s.masks = make([]state.State, len(oldMasks)*2)
	s.subs = make([]uint8, len(oldSubs)*2)
	s.count = 0
	for i, m := range oldMasks {
		if m != 0 {
			s.probeInsert(m, oldSubs[i])
		}
	}
}

// pow2Ceil returns the smallest power of two ≥ n (n ≤ 1 → 1).
func pow2Ceil(n int) int {
	if n <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(n-1))
}

// censusObserver records replica visits at window levels into per-level
// counters indexed by j = bits − d (slot 0 unused). Pass A probes+inserts,
// pass B probes only; the driver runs the passes sequentially, so plain
// uint64 counters need no atomics.
type censusObserver struct {
	keys    memoKeySet
	sym     *symmetry.Symmetry
	visitsA []uint64
	hitsA   []uint64
	visitsB []uint64
	hitsB   []uint64
	depth   int
	window  int
	mode    memoKeyMode
	passA   bool
}

// newCensusObserver returns an observer for one point (mode, window), starting
// in pass A.
func newCensusObserver(mode memoKeyMode, sym *symmetry.Symmetry, keys memoKeySet, depth, window int) *censusObserver {
	return &censusObserver{
		mode: mode, sym: sym, keys: keys, depth: depth, window: window, passA: true,
		visitsA: make([]uint64, window+1), hitsA: make([]uint64, window+1),
		visitsB: make([]uint64, window+1), hitsB: make([]uint64, window+1),
	}
}

// visit records one reached state (st, cur) with nb visited cells. The key is
// the raw pair or its D4 class form; CanonicalClass touches precomputed
// tables only — a constant cost per node (plan 22).
func (o *censusObserver) visit(nb int, st state.State, cur int) {
	j := nb - o.depth
	if j < 1 || j > o.window {
		return
	}
	key, sub := o.stateKey(st, cur)
	if o.passA {
		o.visitsA[j]++
		if o.keys.probeInsert(key, sub) {
			o.hitsA[j]++
		}
		return
	}
	o.visitsB[j]++
	if o.keys.probe(key, sub) {
		o.hitsB[j]++
	}
}

// stateKey maps a state to its census key under the run's mode.
func (o *censusObserver) stateKey(st state.State, cur int) (state.State, uint8) {
	if o.mode == memoKeyRaw {
		return st, uint8(cur)
	}
	K, rep, _ := o.sym.CanonicalClass(st, cur)
	return K, rep
}

// replicaCount is the census copy of searcher.dfsCount (specs/searcher.md):
// same candidates, pruner hook and stop semantics through exported APIs only.
// Two test-only knobs extend the original: an observer fed with every reached
// state, and a cutoff level that stops expansion below d+J — visits at levels
// ≤ d+J are cutoff-independent because the pruner checks a candidate before
// recursing. The pin test TestMemoReplicaMatchesReversalAllDepths5x5 keeps it
// bit-identical to CountPathsWithCacheReversal with both knobs off.
type replicaCount struct {
	graph     *graph.Graph
	sym       *symmetry.Symmetry
	pruner    *pruner.Pruner
	memo      *cache.View
	obs       *censusObserver
	stopLevel int
	cutoff    int
}

// newReplicaCount returns a replica over g counting against memo.
func newReplicaCount(g *graph.Graph, sym *symmetry.Symmetry, memo *cache.View, stopLevel, cutoff int, obs *censusObserver) *replicaCount {
	return &replicaCount{graph: g, sym: sym, pruner: pruner.New(g), memo: memo, stopLevel: stopLevel, cutoff: cutoff, obs: obs}
}

// dfs mirrors searcher.dfsCount; the return value is meaningful only while no
// cutoff fires (the pin test runs with the cutoff disabled).
func (r *replicaCount) dfs(ctx context.Context, st state.State, end int, res *types.Result) int {
	if ctx.Err() != nil {
		return 0
	}

	total := r.graph.GetTotalCells()
	nb := st.CountBits()
	if r.obs != nil {
		r.obs.visit(nb, st, end)
	}

	if nb >= total {
		return 1
	}

	unvisited := st.Invert(total)
	if r.stopLevel >= 0 && nb == r.stopLevel {
		return r.completions(unvisited, end, res)
	}
	if r.cutoff >= 0 && nb >= r.cutoff {
		return 0
	}

	cand := r.graph.GetNeighborMask(end).Intersect(unvisited)

	found := 0
	for n := range cand.AllVisited() {
		newUnvisited := unvisited.Unvisit(n)
		if !newUnvisited.IsEmpty() {
			if cut, reason := r.pruner.ShouldPruneAfterVisit(n, newUnvisited); cut {
				res.CountPrune(reason)
				continue
			}
		}
		found += r.dfs(ctx, st.Visit(n), n, res)
	}
	return found
}

// completions mirrors searcher.(*Searcher).completions: one Get(K) per stop
// call, each candidate end collapsed to its class slot with the frame/stab
// tables of the shared canonicalization.
func (r *replicaCount) completions(unvisited state.State, end int, res *types.Result) int {
	cand := r.graph.GetNeighborMask(end).Intersect(unvisited)
	K, frame, stab := r.sym.CanonicalMaskFrame(unvisited)
	val, maskFound := r.memo.Get(K)

	found := 0
	for u := range cand.AllVisited() {
		if !maskFound {
			res.CacheMisses++
			continue
		}
		rep := r.sym.ClassRep(stab, r.sym.TransformCell(frame, int(u)))
		weight, ok := val.Weight(rep)
		if !ok {
			res.CacheMisses++
			continue
		}
		res.CacheHits++
		found += int(weight / uint64(r.sym.CellOrbitSize(stab, int(rep))))
	}
	return found
}

// memoStopLevel mirrors the stop of CountPathsWithCacheReversal: level
// totalCells−d when the reversal duality is reachable, −1 (full descent)
// otherwise.
func memoStopLevel(totalCells, depth int) int {
	if 2*depth <= totalCells {
		return totalCells - depth
	}
	return -1
}

// memoCensusStats is the result of one census point: sample sizes, aborts,
// the per-level observer counters and the wall time of both passes.
type memoCensusStats struct {
	obs        *censusObserver
	totalTasks int
	tasksA     int
	tasksB     int
	aborted    int
	dur        time.Duration
}

// runMemoCensusPoint measures one full-coverage point end-to-end (stride walk
// plus the two passes) — the wrapper tests drive directly; sampled points go
// through collectMemoSample and runMemoCensusSampled like the benchmark.
func runMemoCensusPoint(ctx context.Context, c *Counter, view *cache.View, depth, window int, mode memoKeyMode, newKeys func(collected int) memoKeySet) memoCensusStats {
	total := countClassTasks(ctx, view)
	sample := collectMemoSample(ctx, view, total, 0)
	return runMemoCensusSampled(ctx, c, view, sample, total, depth, window, mode, newKeys)
}

// runMemoCensusSampled measures one point over an already collected sorted
// sample: pass A descends the even sample indices into a fresh key store,
// pass B reads with the odd ones. The passes run on one thread — counters
// contain no concurrency and no timers. total is the exact class-task
// denominator the sample was drawn from; newKeys is sized to the sample.
func runMemoCensusSampled(ctx context.Context, c *Counter, view *cache.View, sample []cache.Entry, total uint64, depth, window int, mode memoKeyMode, newKeys func(collected int) memoKeySet) memoCensusStats {
	window = min(window, c.graph.GetTotalCells()-2*depth)
	if window < 1 || len(sample) == 0 {
		panic(fmt.Sprintf("memo census: empty point depth=%d window=%d tasks=%d", depth, window, len(sample)))
	}

	obs := newCensusObserver(mode, c.symmetry, newKeys(len(sample)), depth, window)
	rep := newReplicaCount(c.graph, c.symmetry, view, memoStopLevel(c.graph.GetTotalCells(), depth), depth+window, obs)

	start := time.Now()
	tasksA, abortedA := runMemoTasks(ctx, rep, sample, 0)
	obs.passA = false
	tasksB, abortedB := runMemoTasks(ctx, rep, sample, 1)

	return memoCensusStats{
		totalTasks: int(total), tasksA: tasksA, tasksB: tasksB,
		aborted: abortedA + abortedB, dur: time.Since(start), obs: obs,
	}
}

// countClassTasks walks the sealed table once and counts expanded class
// tasks (the exact denominator of the stride).
func countClassTasks(ctx context.Context, view *cache.View) uint64 {
	var total uint64
	for range view.All(ctx) {
		total++
	}
	return total
}

// collectMemoSample takes the shared countcensus stride sample (unbiased, no
// full materialization) and sorts it by class key so the S_A/S_B split is
// reproducible across runs. The sort is plan-22-only: plan 20 measures
// per-task costs where the walk order carries no information.
func collectMemoSample(ctx context.Context, view *cache.View, total uint64, sampleN int) []cache.Entry {
	sampled := strideSample(ctx, view, total, sampleN)
	slices.SortFunc(sampled, memoEntryLess)
	return sampled
}

// memoEntryLess orders entries by class key (Mask, Rep); each class appears
// once in the walk, so the order is total.
func memoEntryLess(a, b cache.Entry) int {
	if c := cmp.Compare(a.Mask, b.Mask); c != 0 {
		return c
	}
	return cmp.Compare(a.Rep, b.Rep)
}

// runMemoTasks descends every other sampled task starting at off. Each task
// runs under the per-task budget insurance: an aborted task keeps its partial
// visits (undercounting, conservative) and is reported.
func runMemoTasks(ctx context.Context, rep *replicaCount, sample []cache.Entry, off int) (tasks, aborted int) {
	for i := off; i < len(sample); i += 2 {
		if ctx.Err() != nil {
			break
		}
		tctx, cancel := context.WithTimeout(ctx, memoCensusTaskBudget)
		var res types.Result
		rep.dfs(tctx, sample[i].Mask, int(sample[i].Rep), &res)
		if tctx.Err() != nil {
			aborted++
		}
		cancel()
		tasks++
	}
	return tasks, aborted
}

// BenchmarkMemoReuseCensus runs the state-reuse census (plan 22): subtests are
// size{N}/depth{D}/j{J}/s{|S|}/{raw|canon}, points come from memoCensusPoints
// and the -bench filter decides which run (ADR-020). Each point descends a
// stride sample of real task-cache class tasks twice — producing half then
// reading half — through replicaCount into one Bloom filter; metrics per
// level and their methodology live in specs/benchmarks.md.
func BenchmarkMemoReuseCensus(b *testing.B) {
	for _, size := range benchmarkSizes {
		b.Run("size"+strconv.Itoa(size), func(b *testing.B) {
			runMemoCensusDepths(b, size)
		})
	}
}

// runMemoCensusDepths generates the task-cache once per capture depth (under
// the pipeline GC scope, ADR-014) and runs all |S|×mode leaves against it;
// the depth line publishes the shared generation cost.
func runMemoCensusDepths(b *testing.B, size int) {
	c := NewCounter(graph.New(size))
	totalCells := c.graph.GetTotalCells()

	for _, pt := range memoCensusPoints[size] {
		window := memoPointWindow(totalCells, pt)
		b.Run("depth"+strconv.Itoa(pt.depth), func(b *testing.B) {
			if c.gcPercent > 0 {
				prev := debug.SetGCPercent(c.gcPercent)
				defer debug.SetGCPercent(prev)
			}
			view, genA, genB := buildMemoTaskCache(b.Context(), c, runtime.NumCPU(), pt.depth)
			b.ReportMetric(ms(genA), "genA_ms/op")
			b.ReportMetric(ms(genB), "genB_ms/op")
			total := countClassTasks(b.Context(), view) // exact stride denominator, once per depth

			b.Run("j"+strconv.Itoa(window), func(b *testing.B) {
				for _, sampleN := range memoCensusLadder {
					b.Run("s"+strconv.Itoa(sampleN), func(b *testing.B) {
						// One sorted sample per (depth, |S|) serves both key modes, so the
						// raw/canon curve difference carries no inter-sample variance; each
						// mode still gets a fresh filter and its own half split.
						sample := collectMemoSample(b.Context(), view, total, sampleN)
						runMemoCensusModes(b, c, view, sample, total, pt.depth, window)
					})
				}
			})
		})
	}
}

// memoPointWindow resolves a point's descent window: the full remainder when
// unset, otherwise capped by it (the census never expands past the stop).
func memoPointWindow(totalCells int, pt memoCensusPoint) int {
	remainder := totalCells - 2*pt.depth
	if pt.window == 0 {
		return remainder
	}
	return min(pt.window, remainder)
}

// runMemoCensusModes runs the raw and canonical key-mode leaves over one
// shared sample; each mode gets a fresh filter, so the curves differ only by
// the key they hash.
func runMemoCensusModes(b *testing.B, c *Counter, view *cache.View, sample []cache.Entry, total uint64, depth, window int) {
	for _, mode := range []memoKeyMode{memoKeyRaw, memoKeyCanonical} {
		b.Run(mode.String(), func(b *testing.B) {
			stats := runMemoCensusSampled(b.Context(), c, view, sample, total, depth, window, mode, newMemoCensusBloom)
			reportMemoCensusMetrics(b, stats)
		})
	}
}

// newMemoCensusBloom sizes the filter to the collected sample: 2^17 bits per
// sampled task under the plan-22 cap.
func newMemoCensusBloom(collected int) memoKeySet {
	return newMemoBloom(min(memoCensusMaxFilterBits, pow2Ceil(max(collected, 1)*memoCensusFilterBitsPerTask)))
}

// reportMemoCensusMetrics publishes the point columns of specs/benchmarks.md:
// aggregates plus per-level h_j (false-positive corrected), multiplicity and
// effective branching.
func reportMemoCensusMetrics(b *testing.B, s memoCensusStats) {
	fpr := s.obs.keys.fpr()
	cut, distinctWin := reportMemoCensusLevels(b, s, fpr)

	b.ReportMetric(float64(s.totalTasks), "totalTasks/op")
	b.ReportMetric(float64(s.tasksA), "tasksA/op")
	b.ReportMetric(float64(s.tasksB), "tasksB/op")
	b.ReportMetric(sumCounter(s.obs.visitsA), "visitsA/op")
	b.ReportMetric(sumCounter(s.obs.visitsB), "visitsB/op")
	b.ReportMetric(float64(distinctWin), "distinctWin/op")
	b.ReportMetric(float64(distinctWin)*memoWindowModelBytes/(1<<20), "winMB32/op")
	b.ReportMetric(100*fpr, "fprPct/op")
	b.ReportMetric(cut, "nodeCutX/op")
	b.ReportMetric(float64(s.aborted), "aborted/op")
	b.ReportMetric(ms(s.dur), "census_ms/op")
	b.ReportMetric(float64(peakRSSBytes())/(1<<20), "peakRSS_MB/op")
}

// reportMemoCensusLevels publishes per-level metrics and returns the node-cut
// projection Π 1/(1−h_j) with the cumulative distinct estimate of pass A.
func reportMemoCensusLevels(b *testing.B, s memoCensusStats, fpr float64) (cut float64, distinctWin uint64) {
	cut = 1
	for j := 1; j < len(s.obs.visitsA); j++ {
		distinct := s.obs.visitsA[j] - s.obs.hitsA[j]
		distinctWin += distinct
		if distinct > 0 {
			b.ReportMetric(float64(s.obs.visitsA[j])/float64(distinct), fmt.Sprintf("mult%d/op", j))
		}
		if s.obs.visitsB[j] == 0 {
			continue
		}
		h := memoCorrectRate(memoRate(s.obs.hitsB[j], s.obs.visitsB[j]), fpr)
		b.ReportMetric(100*h, fmt.Sprintf("h%d_pct/op", j))
		cut *= 1 / (1 - h)
		if j+1 < len(s.obs.visitsB) && s.obs.visitsB[j+1] > 0 {
			br := float64(s.obs.visitsB[j+1]) / float64(s.obs.visitsB[j])
			b.ReportMetric(br*(1-h), fmt.Sprintf("beff%d/op", j))
		}
	}
	return cut, distinctWin
}

// memoRate is hits/visits with a zero guard (0 for an empty level).
func memoRate(hits, visits uint64) float64 {
	if visits == 0 {
		return 0
	}
	return float64(hits) / float64(visits)
}

// memoCorrectRate removes the filter's false positives from a raw hit rate:
// observed = h + (1−h)·fpr, so h = (observed − fpr)/(1 − fpr), floored at 0.
func memoCorrectRate(rawHitRate, fpr float64) float64 {
	if fpr >= 1 {
		return math.NaN()
	}
	return max(0, (rawHitRate-fpr)/(1-fpr))
}

// sumCounter sums a per-level counter slice.
func sumCounter(xs []uint64) float64 {
	var s uint64
	for _, x := range xs {
		s += x
	}
	return float64(s)
}

// buildMemoTaskCache runs the pipeline's generation (gen A + gen B, same calls
// as censusCountOnce) and seals the task-cache; it returns the read view with
// the phase durations.
func buildMemoTaskCache(ctx context.Context, c *Counter, workers, depth int) (*cache.View, time.Duration, time.Duration) {
	m := monitoring.NewFakeMonitor()
	m.Start(ctx)
	intermediate := c.generateIntermediate(ctx, m, workers, depth)
	taskCache := c.extendTasks(ctx, m, workers, intermediate, depth)
	m.Finish()
	view := taskCache.Seal()
	return view, m.Phase("gen A").Duration, m.Phase("gen B").Duration
}

// memoCensusFixture returns a counter and sealed task-cache for one test size
// and depth (default runtime GC; generation is what censusCountOnce does).
func memoCensusFixture(ctx context.Context, t *testing.T, size, depth int) (*Counter, *cache.View) {
	t.Helper()
	c := NewCounter(graph.New(size))
	view, _, _ := buildMemoTaskCache(ctx, c, runtime.NumCPU(), depth)
	return c, view
}

// newExactTestKeys is the exact-store factory for full-coverage test points
// (capacity is only a hint — the set rehashes on demand).
func newExactTestKeys(collected int) memoKeySet {
	return newExactStateSet(max(collected*64, 1024))
}

// newSmallBloomKeys is a fixed small Bloom store factory for determinism and
// unit points (keeps `go test` allocations far from the benchmark budget).
func newSmallBloomKeys(int) memoKeySet { return newMemoBloom(1 << 20) }

// TestMemoReplicaMatchesReversalAllDepths5x5 pins the replica to the real
// dfsCount: with observer and cutoff off it must give, per task at every
// admissible depth, the same total, stop-probe hits/misses and prune counts
// as CountPathsWithCacheReversal on the same sealed table.
func TestMemoReplicaMatchesReversalAllDepths5x5(t *testing.T) {
	const size = 5
	ctx := context.Background()

	for d := 1; d <= size*size/2; d++ {
		t.Run("depth"+strconv.Itoa(d), func(t *testing.T) {
			c, view := memoCensusFixture(ctx, t, size, d)
			rep := newReplicaCount(c.graph, c.symmetry, view, memoStopLevel(size*size, d), -1, nil)

			for e := range view.All(ctx) {
				var res types.Result
				paths := rep.dfs(ctx, e.Mask, int(e.Rep), &res)
				res.Finalize()
				ref := c.searcher.CountPathsWithCacheReversal(ctx, e, view, d)

				assert.Equal(t, ref.TotalPathsFound, paths, "task %+v: total", e)
				assert.Equal(t, ref.CacheHits, res.CacheHits, "task %+v: hits", e)
				assert.Equal(t, ref.CacheMisses, res.CacheMisses, "task %+v: misses", e)
				assert.Equal(t, ref.Pruned, res.Pruned, "task %+v: prunes", e)
			}
		})
	}
}

// TestMemoReplicaCutoffKeepsLevelCounts pins the plan-22 correctness claim
// that visits at levels ≤ d+J do not depend on the cutoff depth: windows J
// and J+1 agree on every level counter up to J.
func TestMemoReplicaCutoffKeepsLevelCounts(t *testing.T) {
	const size, depth = 5, 6
	ctx := context.Background()
	c, view := memoCensusFixture(ctx, t, size, depth)

	lo := runMemoCensusPoint(ctx, c, view, depth, 4, memoKeyRaw, newExactTestKeys)
	hi := runMemoCensusPoint(ctx, c, view, depth, 5, memoKeyRaw, newExactTestKeys)

	assert.Equal(t, lo.obs.visitsA[:5], hi.obs.visitsA[:5], "pass A visits up to the smaller cutoff")
	assert.Equal(t, lo.obs.visitsB[:5], hi.obs.visitsB[:5], "pass B visits up to the smaller cutoff")
}

// TestMemoCensusCountersDeterministic re-runs one full-coverage point and
// requires bit-identical counters: no concurrency, timers or map order leak
// into the census.
func TestMemoCensusCountersDeterministic(t *testing.T) {
	const size, depth = 5, 6
	ctx := context.Background()
	c, view := memoCensusFixture(ctx, t, size, depth)

	first := runMemoCensusPoint(ctx, c, view, depth, 4, memoKeyRaw, newSmallBloomKeys)
	second := runMemoCensusPoint(ctx, c, view, depth, 4, memoKeyRaw, newSmallBloomKeys)

	assert.Equal(t, first.totalTasks, second.totalTasks)
	assert.Equal(t, first.obs.visitsA, second.obs.visitsA)
	assert.Equal(t, first.obs.hitsA, second.obs.hitsA)
	assert.Equal(t, first.obs.visitsB, second.obs.visitsB)
	assert.Equal(t, first.obs.hitsB, second.obs.hitsB)
}

// TestMemoCensusCanonicalKeyNeverLosesUses exact stores: visits do not depend
// on the key mode, and because equal raw states share a canonical class,
// reader hits per level under D4 keys never fall below raw ones.
func TestMemoCensusCanonicalKeyNeverLosesReuse(t *testing.T) {
	const size, depth = 5, 6
	ctx := context.Background()
	c, view := memoCensusFixture(ctx, t, size, depth)

	raw := runMemoCensusPoint(ctx, c, view, depth, 4, memoKeyRaw, newExactTestKeys)
	canon := runMemoCensusPoint(ctx, c, view, depth, 4, memoKeyCanonical, newExactTestKeys)

	assert.Equal(t, raw.obs.visitsB, canon.obs.visitsB, "visits are mode-independent")
	for j := range raw.obs.hitsB {
		assert.GreaterOrEqual(t, canon.obs.hitsB[j], raw.obs.hitsB[j], "level %d", j)
	}
}

// TestMemoCensusExactControlCalibratesBloom runs each control point twice at
// full coverage — exact open-addressing set, then Bloom sized to 8 bits per
// exactly counted distinct state — and requires the corrected repeat rates
// to agree within 2 percentage points per level (plan-22 success metric).
func TestMemoCensusExactControlCalibratesBloom(t *testing.T) {
	cases := []struct {
		size, depth, window int
	}{
		{6, 17, 2}, // remainder 2: levels 18..19 over all tasks
		{6, 14, 3}, // deeper sample of the reuse curve
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("size%d/depth%d/j%d", tc.size, tc.depth, tc.window), func(t *testing.T) {
			ctx := context.Background()
			c, view := memoCensusFixture(ctx, t, tc.size, tc.depth)

			exact := runMemoCensusPoint(ctx, c, view, tc.depth, tc.window, memoKeyRaw, newExactTestKeys)
			distinctExact := memoDistinctTotal(exact.obs)
			bloomBits := pow2Ceil(int(distinctExact*8 + 4096))
			newBloom := func(int) memoKeySet { return newMemoBloom(bloomBits) }
			bloom := runMemoCensusPoint(ctx, c, view, tc.depth, tc.window, memoKeyRaw, newBloom)

			fpr := bloom.obs.keys.fpr()
			assert.Less(t, fpr, 0.05, "filter kept below the overload zone")
			for j := 1; j <= tc.window; j++ {
				if exact.obs.visitsB[j] == 0 {
					continue
				}
				hExact := memoRate(exact.obs.hitsB[j], exact.obs.visitsB[j])
				hBloom := memoCorrectRate(memoRate(bloom.obs.hitsB[j], bloom.obs.visitsB[j]), fpr)
				assert.InDelta(t, hExact, hBloom, 0.02, "level %d repeat rate", j)
			}

			distinctBloom := memoDistinctTotal(bloom.obs)
			assert.GreaterOrEqual(t, float64(distinctBloom), 0.9*float64(distinctExact),
				"distinct estimate may only lose false-positive merges")
		})
	}
}

// memoDistinctTotal sums the pass-A distinct estimates over all levels.
func memoDistinctTotal(obs *censusObserver) uint64 {
	var d uint64
	for j := 1; j < len(obs.visitsA); j++ {
		d += obs.visitsA[j] - obs.hitsA[j]
	}
	return d
}

// TestMemoKeySetSemantics pins both census stores: no false negatives for
// inserted keys, growth keeps everything found; the exact store adds zero
// false positives and the Bloom FPR estimate grows with the fill.
func TestMemoKeySetSemantics(t *testing.T) {
	tests := []struct {
		kind string
	}{
		{"bloom"},
		{"exact"},
	}

	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			set := newMemoTestSet(tc.kind)

			key, sub := state.State(0x1234), uint8(7)
			assert.False(t, set.probe(key, sub), "fresh store misses")
			assert.False(t, set.probeInsert(key, sub), "first insert reports a new key")
			assert.True(t, set.probeInsert(key, sub), "re-insert hits")
			assert.True(t, set.probe(key, sub))

			for i := range 20000 { // crosses the exact set's rehashes
				set.probeInsert(state.State(1+uint64(i)*7919), uint8(i%64))
			}
			for i := range 20000 {
				assert.True(t, set.probe(state.State(1+uint64(i)*7919), uint8(i%64)), "key %d lost", i)
			}

			if tc.kind == "exact" {
				for i := range 5000 { // never inserted keys stay misses
					assert.False(t, set.probe(state.State(1<<40+uint64(i)), uint8((i+3)%64)), "false positive %d", i)
				}
				assert.Zero(t, set.fpr())
			} else {
				assert.Greater(t, set.fpr(), 0.0, "fill is non-trivial after 25k inserts")
				assert.Less(t, set.fpr(), 1.0)
			}
		})
	}
}
