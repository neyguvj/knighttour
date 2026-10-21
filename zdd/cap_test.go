package zdd

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withArenaCap lowers the soft byte ceiling for one test and restores it
// afterwards; tests that use it must not run in parallel (the lever is a
// package-internal global by design — no env or flag handle, ADR-020).
func withArenaCap(t *testing.T, limit int) {
	t.Helper()
	old := arenaCap
	arenaCap = limit
	t.Cleanup(func() { arenaCap = old })
}

// TestArenaCapStopsBuild lowers the byte ceiling below what a small build
// reserves and requires an OverCapError (errors.Is ErrOverCap) carrying live
// node and byte counters instead of a panic or OOM; after restoring the lever
// the same input builds fine — the process stays usable.
func TestArenaCapStopsBuild(t *testing.T) {
	n, edges := 6, completeGraph(6)

	withArenaCap(t, 24<<10) // above the tables' initial reserve, below what K6 reserves
	d, err := Build(n, edges, Paths, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOverCap)
	assert.Nil(t, d)
	var over *OverCapError
	require.ErrorAs(t, err, &over, "typed OverCapError")
	assert.Positive(t, over.Nodes, "live nodes reported at cutoff")
	assert.Positive(t, over.Bytes, "reserved bytes reported at cutoff")
	assert.Greater(t, over.Bytes, over.Cap, "cutoff happens past the cap")

	dCycles, err := Build(n, edges, Cycles, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOverCap)
	assert.Nil(t, dCycles)

	withArenaCap(t, DefaultArenaCap)
	ok, err := Build(n, edges, Paths, nil)
	require.NoError(t, err, "same input builds after restoring the cap")
	assert.Equal(t, uint64(360), ok.Count())
}

// TestOverCapErrorSnapshotsCap raises the global lever back to a huge value
// right after the cutoff and requires the error to still name the cap that
// was in force at the abort — in the field and in the rendered message.
func TestOverCapErrorSnapshotsCap(t *testing.T) {
	n, edges := 6, completeGraph(6)

	withArenaCap(t, 24<<10)
	_, err := Build(n, edges, Paths, nil)
	require.ErrorIs(t, err, ErrOverCap)
	var over *OverCapError
	require.ErrorAs(t, err, &over)

	arenaCap = DefaultArenaCap // lever raised again; t.Cleanup still restores it
	assert.Equal(t, 24<<10, over.Cap, "cap captured at the abort")
	assert.Contains(t, over.Error(), "cap of 24576", "message renders the snapshot, not the live lever")
}

// TestArenaCapNotSpurious checks a generous ceiling never fires on the small
// controls (the default is only exercised for real by the census).
func TestArenaCapNotSpurious(t *testing.T) {
	withArenaCap(t, 1<<24)
	d, err := Build(6, completeGraph(6), Paths, nil)
	require.NoError(t, err)
	assert.Equal(t, uint64(360), d.Count())
}

// TestBudgetElementSizes pins the budget model against the real type layout:
// if znode, memoSlot or the unique-table slot ever change shape, the byte
// accounting in cap.go must be re-derived in the same change.
func TestBudgetElementSizes(t *testing.T) {
	assert.Equal(t, nodeBytes, int(reflect.TypeFor[znode]().Size()))
	assert.Equal(t, memoSlotBytes, int(reflect.TypeFor[memoSlot]().Size()))
	assert.Equal(t, uniqSlotBytes, int(reflect.TypeFor[uint32]().Size()))
}

// TestMemoKeyOffsetWidth pins the memo key offset to 64 bits: the default byte
// budget lets the key arena grow past 4 GiB before the ceiling trips (the key
// share of the measured 6×6 Paths build exceeds 2^32 bytes), so a 32-bit
// offset would wrap and take get() out of range.
func TestMemoKeyOffsetWidth(t *testing.T) {
	assert.Equal(t, 64, reflect.TypeOf(memoSlot{}.keyOff).Bits())
}
