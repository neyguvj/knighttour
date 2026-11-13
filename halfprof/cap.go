package halfprof

import (
	"errors"
	"fmt"
)

// ErrOverCap is the sentinel returned (via errors.Is) when an aggregation
// exceeds the soft byte ceiling and stops before exhausting process memory.
var ErrOverCap = errors.New("halfprof: aggregation exceeded the soft byte cap")

// OverCapError reports an aborted aggregation at the byte ceiling. Side is
// "upper" or "lower", Entries the live table size at the cutoff, Bytes the
// reserved budget requested by the refused growth and Cap the ceiling in
// force when the run was stopped — captured here so the message stays
// truthful after tests move the global lever again.
type OverCapError struct {
	Side    string
	Entries int
	Bytes   int
	Cap     int
}

// Error describes the cutoff from the counters and the cap captured at abort time.
func (e *OverCapError) Error() string {
	return fmt.Sprintf("halfprof: %s table with %d entries needs %d reserved bytes, cap %d",
		e.Side, e.Entries, e.Bytes, e.Cap)
}

// Unwrap ties the error to ErrOverCap so errors.Is identifies the condition.
func (e *OverCapError) Unwrap() error { return ErrOverCap }

// DefaultSoftCapBytes is the initial soft ceiling on the summed reserved bytes
// of the two aggregation tables of one group run. It keeps even a budget-
// filling run inside a 36 GB census machine; the census calibration (plan 30)
// may move it with measured numbers.
const DefaultSoftCapBytes = 16 << 30

// softCap is the internal soft ceiling in reserved bytes (no env or flag
// handle, ADR-020); tests lower it directly and restore it afterwards. It
// must not be changed while a count runs on another goroutine.
var softCap = DefaultSoftCapBytes

// entryReserve is the per-entry byte allowance of an aggregation table:
// profile key (mask word plus pairing bytes) with map overhead, charged to
// every distinct profile before insertion.
const entryReserve = 128

// memBudget accounts reserved bytes against the soft limit. reserve fails
// with a capHit naming the requested total when the limit would be crossed.
type memBudget struct{ used int64 }

// reserve adds extra bytes to the budget, failing when the soft limit crosses.
func (b *memBudget) reserve(extra int64) error {
	if b.used+extra > int64(softCap) {
		return &capHit{need: b.used + extra}
	}
	b.used += extra
	return nil
}

// release returns no-longer-needed bytes to the budget (flat DP generation
// arrays swap their backing store under this accounting).
func (b *memBudget) release(n int64) { b.used -= n }

// capHit marks a refused budget growth; Unwrap binds it to ErrOverCap.
type capHit struct{ need int64 }

func (c *capHit) Error() string { return fmt.Sprintf("halfprof: %d reserved bytes exceed cap", c.need) }
func (c *capHit) Unwrap() error { return ErrOverCap }
