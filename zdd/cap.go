package zdd

import (
	"errors"
	"fmt"
)

// ErrOverCap is the sentinel returned (via errors.Is) when a build exceeds the
// soft byte ceiling and stops before exhausting process memory.
var ErrOverCap = errors.New("zdd: arena capacity exceeded")

// OverCapError reports an aborted build at the byte ceiling. Nodes and Bytes
// are the live node count and the reserved budget bytes at the moment of the
// cutoff; Cap is the ceiling in force when the build was stopped — captured
// here so the message stays truthful after tests move the global lever again.
type OverCapError struct {
	Nodes int // live arena nodes at the cutoff (terminals included, sentinel excluded)
	Bytes int // reserved budget bytes of all build structures at the cutoff
	Cap   int // ceiling in force when the build was stopped
}

// Error describes the cutoff from the counters and the cap captured at abort
// time; it never reads the global lever.
func (e *OverCapError) Error() string {
	return fmt.Sprintf("zdd: %d live nodes and %d reserved bytes exceed the arena cap of %d",
		e.Nodes, e.Bytes, e.Cap)
}

// Unwrap ties the error to ErrOverCap so errors.Is identifies the condition.
func (e *OverCapError) Unwrap() error { return ErrOverCap }

// Element sizes of the build structures; a test pins them against the real
// type sizes so the budget model cannot drift from the layout silently.
const (
	nodeBytes     = 24 // znode: variable/low/high uint32 + weight uint64
	uniqSlotBytes = 4  // unique-table slot: one uint32 arena id
	memoSlotBytes = 16 // memo slot: uint64 key offset + uint32 length + uint32 id
)

// DefaultArenaCap is the initial soft ceiling on the summed reserved bytes of
// the build structures (builder.liveBytes). The memo table dominates memory,
// not the node arena: the measured 6×6 Paths narrow-order build keeps only
// ~2.5·10^5 live nodes but ~2.5·10^8 memo entries — a memo:nodes ratio near
// 10^3, because every dead-end state is memoized while zero-suppression
// keeps almost none of them as nodes. A node-count ceiling therefore never
// fires where memory actually runs out (the old default of 2^28 live nodes
// corresponds to ~15 GB of memo bytes on that point).
//
// Per-memo-entry layout, measured at a 2 GiB probe cutoff of the same point
// (50.3M entries): slot array ~21 B amortised at the 0.75 load factor + packed
// key ~15 B + append spare ≈ 61 reserved bytes per entry; the full build
// (~250M entries) needs ≈ 15 GB of live bytes.
//
// One growth step overshoots the ceiling by at most one table doubling (the
// check runs after the step), and the realloc transients keep the previous
// slot array and key arena resident until the GC frees them: measured peak
// RSS ≈ 1.8× reserved bytes at the cutoff (the census-order points; bad edge
// orders reach higher transients). The default 16 GiB keeps even a
// budget-filling build under up to ~34 GB — inside the 36 GB census machine
// with margin for the runtime — while leaving room above the ~15 GB needed by
// the measured full 6×6 Paths point.
const DefaultArenaCap = 1 << 34

// arenaCap is the internal soft ceiling in budget bytes (no env or flag
// handle, ADR-020); tests lower it directly and restore it afterwards. It
// must not be changed while a build runs on another goroutine.
var arenaCap = DefaultArenaCap

// liveBytes returns the reserved bytes of every build structure: the node
// arena, the unique-table slots and the memo table's key arena and slots.
// Capacities count, not lengths — the reserve is what the resident set sees,
// and both append-style arenas keep spare capacity between growth steps.
func (b *builder) liveBytes() int64 {
	return int64(cap(b.a.nodes))*nodeBytes +
		int64(cap(b.uniq.slots))*uniqSlotBytes +
		int64(cap(b.memo.keys)) +
		int64(cap(b.memo.slots))*memoSlotBytes
}

// overCap returns the cutoff error once the reserved bytes have crossed
// arenaCap, nil while inside budget. The build path calls it after every step
// that can grow a structure (node allocation, key append, table doubling),
// so an overshoot never exceeds one such step plus the realloc transient of
// the arrays being doubled.
func (b *builder) overCap() error {
	live := b.liveBytes()
	if live <= int64(arenaCap) {
		return nil
	}
	return &OverCapError{Nodes: len(b.a.nodes) - 1, Bytes: int(live), Cap: arenaCap}
}
