package split

import (
	"errors"
	"fmt"
)

// ErrOverCap is the sentinel returned (via errors.Is) when a build exceeds the
// soft byte ceiling and stops before exhausting process memory.
var ErrOverCap = errors.New("split: build exceeded the soft byte cap")

// OverCapError reports an aborted half-DP/gluing build at the byte ceiling.
// Layer is the step of the current half's DP at the cutoff, States the live
// table entries (both generations plus a retained σ table), Bytes the reserved
// budget requested by the refused growth and Cap the ceiling in force when the
// build was stopped — captured here so the message stays truthful after tests
// move the global lever again.
type OverCapError struct {
	Layer  int
	States int
	Bytes  int
	Cap    int
}

// Error describes the cutoff from the counters and the cap captured at abort time.
func (e *OverCapError) Error() string {
	return fmt.Sprintf("split: layer %d with %d live states needs %d reserved bytes, cap %d",
		e.Layer, e.States, e.Bytes, e.Cap)
}

// Unwrap ties the error to ErrOverCap so errors.Is identifies the condition.
func (e *OverCapError) Unwrap() error { return ErrOverCap }

// DefaultSoftCapBytes is the initial soft ceiling on the summed reserved bytes
// of the build structures (the two live DP generations and any retained σ
// table). It keeps even a budget-filling run inside a 36 GB census machine;
// the census calibration (plan 26) may move it with measured numbers.
const DefaultSoftCapBytes = 16 << 30

// softCap is the internal soft ceiling in reserved bytes (no env or flag handle,
// ADR-020); tests lower it directly and restore it afterwards. It must not be
// changed while a count runs on another goroutine.
var softCap = DefaultSoftCapBytes

// capHit marks a refused budget growth and carries the requested total so the
// engine can report it as Bytes after filling in the layer progress.
type capHit struct{ need int64 }

func (c *capHit) Error() string { return fmt.Sprintf("split: %d reserved bytes exceed cap", c.need) }
func (c *capHit) Unwrap() error { return ErrOverCap }

// memBudget accounts the reserved bytes of every build structure against the
// soft limit. Reserving before allocating keeps the growth transient (old plus
// new arrays during a rehash) inside the cap.
type memBudget struct{ used int64 }

// reserve adds extra bytes to the budget, failing with a capHit naming the
// requested total when the soft limit would be crossed.
func (b *memBudget) reserve(extra int64) error {
	if b.used+extra > int64(softCap) {
		return &capHit{need: b.used + extra}
	}
	b.used += extra
	return nil
}

// release returns no-longer-needed bytes to the budget.
func (b *memBudget) release(n int64) { b.used -= n }
