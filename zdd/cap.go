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
// type sizes so the budget model cannot drift from the layout silently. The
// per-level memo tables need no constant: their reserved bytes are summed
// incrementally into memoTables.bytes as slot arrays are allocated.
const (
	nodeBytes     = 24 // znode: variable/low/high uint32 + weight uint64
	uniqSlotBytes = 4  // unique-table slot: one uint32 arena id
)

// DefaultArenaCap is the initial soft ceiling on the summed reserved bytes of
// the build structures (builder.liveBytes). The memo tables dominate memory,
// not the node arena: nearly every visited frontier state becomes a memo
// entry, while zero-suppression keeps only a fraction of them as nodes. A
// node-count ceiling therefore never fires where memory actually runs out.
//
// Per-memo-entry budget under the plan-24 lever-A layout: one slot per entry
// at stride 4 B id + (frontier width + 1) key bytes, grown only past a load
// of 0.875 and with no separate key arena — ≈ stride/0.875 reserved bytes per
// entry. The ADR-040 layout before it cost ≈ 61 B per entry (16 B slot array
// at load 0.75 + a ~15 B packed-key arena with append spare); the measured
// numbers of the current layout are recorded in ADR-041.
//
// One growth step overshoots the ceiling by at most one table doubling (the
// check runs after the step), and the realloc transients keep the previous
// slot array resident until the GC frees them: measured peak RSS ≈ 1.8×
// reserved bytes at the cutoff (the census-order points; bad edge orders
// reach higher transients). The default 16 GiB keeps even a budget-filling
// build under up to ~34 GB — inside the 36 GB census machine with margin for
// the runtime.
const DefaultArenaCap = 1 << 34

// arenaCap is the internal soft ceiling in budget bytes (no env or flag
// handle, ADR-020); tests lower it directly and restore it afterwards. It
// must not be changed while a build runs on another goroutine.
var arenaCap = DefaultArenaCap

// liveBytes returns the reserved bytes of every build structure: the node
// arena, the unique-table slots and the per-level memo tables. Capacities
// count, not lengths — the reserve is what the resident set sees; the memo
// part is the running allocation sum maintained by memoTables.reserve.
func (b *builder) liveBytes() int64 {
	return int64(cap(b.a.nodes))*nodeBytes +
		int64(cap(b.uniq.slots))*uniqSlotBytes +
		b.memo.bytes
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
