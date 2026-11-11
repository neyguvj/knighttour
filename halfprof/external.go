package halfprof

// external.go — the external-merge path (plan 30 component 4): one enumeration
// per half spills sorted runs to disk, each half's runs are merged into a
// single globally sorted, aggregated run, and the two final runs are joined by
// mask with the same cycle test as the in-memory join. The group total is
// bit-identical to CountGroup; live memory stays bounded by the spill threshold
// plus one merge buffer instead of the whole table.

import (
	"cmp"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"

	"knighttour/workerpool"
)

// mergeFanIn bounds the number of input streams a single merge level reads, so
// a half with more runs than this merges in levels (bounded file handles).
const mergeFanIn = 32

// ExternalOptions configures CountGroupExternal/CountBoardExternal. SpillBytes
// is the live aggregation ceiling in bytes that triggers a sorted run flush
// (must be > 0); Dir is the working directory for runs ("" → os.TempDir);
// Preserve keeps the run files after the count for debugging.
type ExternalOptions struct {
	Dir        string
	SpillBytes int
	Preserve   bool
}

// ExternalStats is an external run's snapshot: the profile/join metrics plus
// how many runs were spilled during enumeration and the peak on-disk bytes of
// live runs at any moment.
type ExternalStats struct {
	Census
	Spills        int
	DiskPeakBytes uint64
}

// CountGroupExternal counts one group by the external-merge path; the total is
// bit-identical to CountGroup for any SpillBytes (specs/halfprof.md).
func CountGroupExternal(ctx context.Context, n, s, t int, opts ExternalOptions) (total uint64, stats ExternalStats, err error) {
	if err = validateN(n); err != nil {
		return 0, ExternalStats{}, err
	}
	if err = validateEndpoints(n, s, t); err != nil {
		return 0, ExternalStats{}, err
	}
	if opts.SpillBytes <= 0 {
		return 0, ExternalStats{}, fmt.Errorf("halfprof: SpillBytes %d must be positive", opts.SpillBytes)
	}
	g := newCutGeom(n)
	sp, err := newSpool(opts.Dir)
	if err != nil {
		return 0, ExternalStats{}, err
	}
	defer func() {
		if !opts.Preserve {
			err = errors.Join(err, os.RemoveAll(sp.dir))
		}
	}()

	total, cen, err := countGroupExternal(ctx, g, sp, s, t, opts.SpillBytes)
	if err != nil {
		return 0, ExternalStats{}, err
	}
	return total, ExternalStats{Census: cen, Spills: sp.spills, DiskPeakBytes: sp.peakDisk}, nil
}

// CountBoardExternal is CountBoard counted group-by-group via the external path;
// the result matches CountBoard and is independent of workers. Each group gets
// its own working subdirectory under opts.Dir.
func CountBoardExternal(ctx context.Context, n, workers int, opts ExternalOptions) (uint64, error) {
	if err := validateN(n); err != nil {
		return 0, err
	}
	groups := Groups(n)
	counts := make([]uint64, len(groups))
	errs := make([]error, len(groups))
	pool := workerpool.New(max(workers, 1))
	pool.Run(ctx, len(groups), func(ctx context.Context, idx iter.Seq[int]) {
		for i := range idx {
			counts[i], _, errs[i] = CountGroupExternal(ctx, n, groups[i].S, groups[i].T, opts)
		}
	})
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	for _, err := range errs {
		if err != nil {
			return 0, err
		}
	}
	var total uint64
	for i, gr := range groups {
		total += uint64(gr.K) * counts[i]
	}
	return 2 * total, nil
}

// countGroupExternal enumerates both halves into spilling tables, merges each
// to a single sorted run and joins them; it returns the group total and census.
func countGroupExternal(ctx context.Context, g *cutGeom, sp *spool, s, t, spillBytes int) (uint64, Census, error) {
	upRef, err := enumerateHalfExternal(ctx, g, sp, false, s, t, spillBytes)
	if err != nil {
		return 0, Census{}, err
	}
	loRef, err := enumerateHalfExternal(ctx, g, sp, true, s, t, spillBytes)
	if err != nil {
		return 0, Census{}, err
	}
	total, joins, err := joinExternalRuns(ctx, upRef, loRef)
	if err != nil {
		return 0, Census{}, err
	}
	cen := Census{
		ProfilesU:       upRef.entries,
		ProfilesL:       loRef.entries,
		Joins:           joins,
		MaxProfileCount: max(upRef.maxW, loRef.maxW),
	}
	return total, cen, nil
}

// enumerateHalfExternal enumerates one half into a spillTable and merges its
// runs to a single aggregated run (a lone run is already complete). The zero
// ref means an empty half.
func enumerateHalfExternal(ctx context.Context, g *cutGeom, sp *spool, lower bool, s, t, spillBytes int) (runRef, error) {
	side := "upper"
	if lower {
		side = "lower"
	}
	tab := newSpillTable(side, sp, spillBytes)
	if err := runHalf(g, ctx, lower, s, t, 0, 1, tab); err != nil {
		return runRef{}, err
	}
	runs, err := tab.finish()
	if err != nil {
		return runRef{}, err
	}
	return mergeHalf(ctx, sp, side, runs)
}

// mergeHalf reduces a half's sorted runs to one globally sorted run that sums
// equal keys; it returns the final ref (entries and maxW describe the merged
// stream). Zero or one input needs no rewrite. ctx is checked per level.
func mergeHalf(ctx context.Context, sp *spool, side string, runs []runRef) (runRef, error) {
	for len(runs) > 1 {
		if err := ctx.Err(); err != nil {
			return runRef{}, err
		}
		next := make([]runRef, 0, (len(runs)+mergeFanIn-1)/mergeFanIn)
		for i := 0; i < len(runs); {
			hi := min(i+mergeFanIn, len(runs))
			grp := runs[i:hi]
			i = hi
			if len(grp) == 1 {
				next = append(next, grp[0])
				continue
			}
			out, err := mergeRuns(ctx, sp, side, grp)
			if err != nil {
				return runRef{}, err
			}
			next = append(next, out)
		}
		runs = next
	}
	if len(runs) == 0 {
		return runRef{}, nil
	}
	return runs[0], nil
}

// mergeRuns k-way merges grp into one sorted run, summing equal keys as they
// surface; it consumes (deletes) its inputs and accounts the output run. Input
// files are opened once and closed together at the end (fan-in bounds handles).
func mergeRuns(ctx context.Context, sp *spool, side string, grp []runRef) (runRef, error) {
	cursors := make([]*cursor, 0, len(grp))
	h := make(cursorHeap, 0, len(grp))
	for _, r := range grp {
		cur, err := openCursor(r)
		if err != nil {
			return runRef{}, errors.Join(err, closeCursors(cursors))
		}
		cursors = append(cursors, cur)
		if cur.ok {
			h = append(h, cur)
		}
	}
	heap.Init(&h)

	rw, path, err := createRun(sp, side)
	if err != nil {
		return runRef{}, errors.Join(err, closeCursors(cursors))
	}
	out, derr := drainMerge(ctx, &h, rw)
	cerr := closeCursors(cursors)
	if derr != nil {
		return runRef{}, errors.Join(derr, rw.discard(), os.Remove(path), cerr)
	}
	size, err := rw.close()
	if err != nil {
		return runRef{}, errors.Join(err, cerr)
	}
	ref := runRef{path: path, size: size, entries: out.n, maxW: out.maxW}
	sp.addRun(size)
	for _, in := range grp {
		if rerr := sp.remove(in); rerr != nil {
			return runRef{}, errors.Join(rerr, cerr)
		}
	}
	return ref, nil
}

// mergeOut accumulates the emitted distinct-record count and max weight.
type mergeOut struct {
	n    int
	maxW uint64
}

// drainMerge pops cursors in key order, coalescing equal keys into single rows
// written to rw, and reports how many distinct records were emitted.
func drainMerge(ctx context.Context, h *cursorHeap, rw *runWriter) (mergeOut, error) {
	var out mergeOut
	var prev profKey
	havePrev := false
	var acc uint64
	for h.Len() > 0 {
		if out.n&(joinTick-1) == 0 {
			if err := ctx.Err(); err != nil {
				return out, err
			}
		}
		c := (*h)[0]
		if havePrev && c.k == prev {
			acc += c.w
		} else {
			if havePrev {
				if err := out.emit(rw, prev, acc); err != nil {
					return out, err
				}
			}
			prev, acc = c.k, c.w
			havePrev = true
		}
		if err := advanceCursor(h); err != nil {
			return out, err
		}
	}
	if havePrev {
		if err := out.emit(rw, prev, acc); err != nil {
			return out, err
		}
	}
	return out, nil
}

// emit writes one aggregated row and updates the running bookkeeping.
func (o *mergeOut) emit(rw *runWriter, k profKey, w uint64) error {
	if err := rw.record(k, w); err != nil {
		return err
	}
	o.n++
	o.maxW = max(o.maxW, w)
	return nil
}

// advanceCursor pops the heap top after advancing its reader; exhausted cursors
// are dropped so the heap shrinks toward empty. Files stay open for the caller's
// closing sweep (fan-in bounds how many are live).
func advanceCursor(h *cursorHeap) error {
	c := (*h)[0]
	if err := c.advance(); err != nil {
		heap.Pop(h)
		return err
	}
	if !c.ok { // cleanly exhausted: remove it from the merge
		heap.Pop(h)
		return nil
	}
	heap.Fix(h, 0)
	return nil
}

// closeCursors closes every opened cursor's file, joining any errors.
func closeCursors(cs []*cursor) error {
	var errs error
	for _, c := range cs {
		errs = errors.Join(errs, c.rr.close())
	}
	return errs
}

// cursor is one run's read position for the merge heap: its current record and
// a flag that it still has data.
type cursor struct {
	rr *runReader
	k  profKey
	w  uint64
	ok bool
}

// openCursor opens r and reads its first record into the cursor.
func openCursor(r runRef) (*cursor, error) {
	rr, err := openRun(r.path)
	if err != nil {
		return nil, err
	}
	c := &cursor{rr: rr}
	if aerr := c.advance(); aerr != nil {
		return nil, errors.Join(aerr, rr.close())
	}
	return c, nil
}

// advance reads the next record; io.EOF marks exhaustion (ok=false).
func (c *cursor) advance() error {
	k, w, err := c.rr.next()
	if errors.Is(err, io.EOF) {
		c.ok = false
		return nil
	}
	if err != nil {
		return err
	}
	c.k, c.w, c.ok = k, w, true
	return nil
}

// cursorHeap orders cursors by full profile key; equal keys coalesce in the
// drain so their relative order is irrelevant to the summed output.
type cursorHeap []*cursor

func (h cursorHeap) Len() int           { return len(h) }
func (h cursorHeap) Less(i, j int) bool { return cmpKey(h[i].k, h[j].k) < 0 }
func (h cursorHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *cursorHeap) Push(x any) {
	c, ok := x.(*cursor)
	if !ok {
		panic("halfprof: cursorHeap.Push received non-*cursor")
	}
	*h = append(*h, c)
}
func (h *cursorHeap) Pop() any {
	old := *h
	n := len(old)
	c := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return c
}

// cmpKey orders profile keys by mask then pairing bytes — the merge's total order.
func cmpKey(a, b profKey) int {
	if c := cmp.Compare(a.K, b.K); c != 0 {
		return c
	}
	return cmp.Compare(a.P, b.P)
}

// joinExternalRuns merges the two final sorted runs by mask and crosses equal
// mask groups with the shared cycle test. A path of "" is an empty stream.
func joinExternalRuns(ctx context.Context, up, lo runRef) (total, joins uint64, err error) {
	us, err := openMaskRun(up.path)
	if err != nil {
		return 0, 0, err
	}
	ls, err := openMaskRun(lo.path)
	if err != nil {
		return 0, 0, errors.Join(err, us.close())
	}
	defer func() { err = errors.Join(err, us.close(), ls.close()) }()

	for us.ok && ls.ok {
		if cerr := ctx.Err(); cerr != nil {
			return 0, 0, cerr
		}
		curK := min(us.k.K, ls.k.K)
		ug := collectMask(us, curK)
		lg := collectMask(ls, curK)
		if len(ug) > 0 && len(lg) > 0 {
			sum, n, jerr := crossRuns(ctx, ug, lg)
			if jerr != nil {
				return 0, 0, jerr
			}
			total, joins = total+sum, joins+n
		}
	}
	if us.err != nil {
		return 0, 0, us.err
	}
	return total, joins, ls.err
}

// collectMask drains the stream while its head keeps mask k, returning those
// entries in pairing order (the crossRuns input order).
func collectMask(s *maskRun, k uint64) []entry {
	var out []entry
	for s.ok && s.k.K == k {
		out = append(out, entry{k: s.k, w: s.w})
		s.advance()
	}
	return out
}

// maskRun is a runReader with one-record lookahead for the join walk.
type maskRun struct {
	rr   *runReader
	err  error
	k    profKey
	w    uint64
	ok   bool
	done bool
}

// openMaskRun opens a final run for joining; an empty path yields a closed
// stream rather than an error (an empty half contributes nothing).
func openMaskRun(path string) (*maskRun, error) {
	if path == "" {
		return &maskRun{done: true}, nil
	}
	rr, err := openRun(path)
	if err != nil {
		return nil, err
	}
	s := &maskRun{rr: rr}
	s.advance()
	return s, nil
}

// advance loads the next record; EOF ends the stream cleanly.
func (s *maskRun) advance() {
	if s.done || s.err != nil {
		return
	}
	k, w, err := s.rr.next()
	if errors.Is(err, io.EOF) {
		s.done, s.ok = true, false
		return
	}
	if err != nil {
		s.err, s.ok = err, false
		return
	}
	s.k, s.w, s.ok = k, w, true
}

// close releases the file handle, reporting any close error.
func (s *maskRun) close() error {
	if s.rr == nil {
		return nil
	}
	return s.rr.close()
}
