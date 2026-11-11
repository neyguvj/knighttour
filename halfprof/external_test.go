package halfprof

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// external thresholds for the equivalence sweep: huge never spills, medium and
// minimal force several runs (minimal also crosses the merge fan-in so the
// level-reduction path runs). They are sized in bytes so run counts stay small
// even on 6×6 tables.
const (
	xHuge    = 1 << 60
	xMedium  = 1 << 20   // 1 MiB
	xMinimal = 16 << 10  // 16 KiB — aggressive on the small 5×5 tables
	xMany    = 128 << 10 // 128 KiB — bounded many-run point for the 6×6 sweep
)

// writeTestRun writes a sorted run of the given rows into sp and returns its ref.
func writeTestRun(t *testing.T, sp *spool, side string, rows []entry) runRef {
	t.Helper()
	rw, path, err := createRun(sp, side)
	require.NoError(t, err)
	ref := runRef{path: path, entries: len(rows)}
	for _, e := range rows {
		require.NoError(t, rw.record(e.k, e.w))
		ref.maxW = max(ref.maxW, e.w)
	}
	size, err := rw.close()
	require.NoError(t, err)
	ref.size = size
	sp.addRun(size)
	return ref
}

// readTestRun reads a run back into a key→weight map (for assertions).
func readTestRun(t *testing.T, path string) map[profKey]uint64 {
	t.Helper()
	rr, err := openRun(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, rr.close()) }()
	out := map[profKey]uint64{}
	for {
		k, w, err := rr.next()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		out[k] = w
	}
}

// TestRunFormatRoundTrip pins the versioned run codec: records read back equal,
// including empty pairings, and EOF terminates cleanly.
func TestRunFormatRoundTrip(t *testing.T) {
	sp, err := newSpool(t.TempDir())
	require.NoError(t, err)

	want := []entry{
		{k: profKey{K: 0, P: ""}, w: 1},
		{k: profKey{K: 5, P: "\x01\x02"}, w: 300},
		{k: profKey{K: ^uint64(0), P: string([]byte{0, 1, 2, 3})}, w: 1 << 40},
	}
	rw, path, err := createRun(sp, "upper")
	require.NoError(t, err)
	for _, r := range want {
		require.NoError(t, rw.record(r.k, r.w))
	}
	_, err = rw.close()
	require.NoError(t, err)

	got := readTestRun(t, path)
	require.Len(t, got, len(want))
	for _, r := range want {
		assert.Equal(t, r.w, got[r.k])
	}
}

// TestMergeHalfAggregatesLevels drives mergeHalf with more runs than the fan-in
// so it merges in levels, and checks equal keys sum across runs into one sorted
// stream while consumed inputs are deleted.
func TestMergeHalfAggregatesLevels(t *testing.T) {
	sp, err := newSpool(t.TempDir())
	require.NoError(t, err)

	const nRuns = mergeFanIn + 8 // forces at least two merge levels
	shared := profKey{K: 7, P: "s"}
	want := map[profKey]uint64{shared: 0}
	runs := make([]runRef, 0, nRuns)
	for i := range nRuns {
		rows := []entry{
			{k: shared, w: uint64(i + 1)},
			{k: profKey{K: uint64(100 + i), P: "x"}, w: uint64(i)},
		}
		want[shared] += uint64(i + 1)
		want[rows[1].k] = uint64(i)
		runs = append(runs, writeTestRun(t, sp, "upper", rows))
	}

	final, err := mergeHalf(context.Background(), sp, "upper", runs)
	require.NoError(t, err)

	got := readTestRun(t, final.path)
	assert.Equal(t, want, got)
	assert.Equal(t, len(want), final.entries)
	assert.Equal(t, want[shared], final.maxW) // the shared key carries the largest aggregated weight

	// Only the single merged run survives; every input was consumed.
	left, err := filepath.Glob(filepath.Join(sp.dir, "*.run"))
	require.NoError(t, err)
	assert.Len(t, left, 1)
}

// TestExternalEqualsInMemory5x5 is the core equivalence: for every non-zero
// endpoint orbit of 5×5 and thresholds {huge, medium, minimal}, CountGroupExternal
// equals CountGroup bit-for-bit (specs/halfprof.md «внешний путь»).
func TestExternalEqualsInMemory5x5(t *testing.T) {
	base := make(map[[2]int]uint64)
	for _, gr := range Groups(5) {
		v, err := CountGroup(context.Background(), 5, gr.S, gr.T)
		require.NoError(t, err)
		if v == 0 {
			continue // zero orbits carry no join to reproduce
		}
		base[[2]int{gr.S, gr.T}] = v
	}
	for _, spill := range []int{xHuge, xMedium, xMinimal} {
		t.Run("spill"+strconv.Itoa(spill), func(t *testing.T) {
			opts := ExternalOptions{SpillBytes: spill, Dir: t.TempDir()}
			for pair, want := range base {
				got, _, err := CountGroupExternal(context.Background(), 5, pair[0], pair[1], opts)
				require.NoError(t, err)
				assert.Equalf(t, want, got, "group (%d,%d) at spill %d", pair[0], pair[1], spill)
			}
		})
	}
}

// TestBoardExternal5x5: the external board total matches CountBoard (1728).
func TestBoardExternal5x5(t *testing.T) {
	got, err := CountBoardExternal(context.Background(), 5, 3, ExternalOptions{SpillBytes: xMedium, Dir: t.TempDir()})
	require.NoError(t, err)
	assert.Equal(t, uint64(1728), got)
}

// TestExternalSpillsAndCleansUp asserts a spilling run writes runs, reports
// them, and removes the working directory on success.
func TestExternalSpillsAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	total, st, err := CountGroupExternal(context.Background(), 6, 0, 8, ExternalOptions{SpillBytes: xMedium, Dir: dir})
	require.NoError(t, err)
	assert.Positive(t, total)
	assert.Positive(t, st.Spills)
	assert.Positive(t, st.DiskPeakBytes)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "working subdirs must be removed after the count")
}

// TestExternalPreserveKeepsRuns: Preserve leaves a working dir with run files.
func TestExternalPreserveKeepsRuns(t *testing.T) {
	dir := t.TempDir()
	_, _, err := CountGroupExternal(context.Background(), 6, 0, 8, ExternalOptions{SpillBytes: xMedium, Dir: dir, Preserve: true})
	require.NoError(t, err)

	subdirs, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, subdirs, 1)
	runs, err := filepath.Glob(filepath.Join(dir, subdirs[0].Name(), "*.run"))
	require.NoError(t, err)
	assert.NotEmpty(t, runs, "preserved run files must remain")
}

// TestExternalInvalidSpill: a non-positive threshold is a caller error.
func TestExternalInvalidSpill(t *testing.T) {
	_, _, err := CountGroupExternal(context.Background(), 5, 0, 6, ExternalOptions{SpillBytes: 0})
	require.Error(t, err)
}

// TestExternalTerminatedContext: a dead ctx aborts without panic.
func TestExternalTerminatedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := CountGroupExternal(ctx, 6, 0, 8, ExternalOptions{SpillBytes: xMedium, Dir: t.TempDir()})
	require.ErrorIs(t, err, context.Canceled)
}

// TestExternalMidEnumerationCancellation trips the fuse during an external count
// of a 6×6 group (dense enumeration polls ctx) and expects context.Canceled.
func TestExternalMidEnumerationCancellation(t *testing.T) {
	fuse := &fuseCtx{Context: context.Background(), limit: 3}
	_, _, err := CountGroupExternal(fuse, 6, 0, 8, ExternalOptions{SpillBytes: xMedium, Dir: t.TempDir()})
	require.ErrorIs(t, err, context.Canceled)
	assert.Greater(t, fuse.calls, 3)
}

// TestExternalMidMergeCancellation forces cancellation during the merge phase by
// pre-building many runs and cancelling the merge ctx.
func TestExternalMidMergeCancellation(t *testing.T) {
	sp, err := newSpool(t.TempDir())
	require.NoError(t, err)
	runs := make([]runRef, 0, mergeFanIn+8)
	for i := range mergeFanIn + 8 {
		rows := make([]entry, 0, 4)
		for j := range 4 {
			rows = append(rows, entry{k: profKey{K: uint64(i*10 + j), P: "p"}, w: 1})
		}
		slices.SortFunc(rows, func(a, b entry) int { return cmpKey(a.k, b.k) })
		runs = append(runs, writeTestRun(t, sp, "upper", rows))
	}
	fuse := &fuseCtx{Context: context.Background(), limit: 1}
	_, err = mergeHalf(fuse, sp, "upper", runs)
	require.ErrorIs(t, err, context.Canceled)
}

// TestRunFormatRejectsBadHeader checks that a foreign magic or version is refused
// rather than misread as records.
func TestRunFormatRejectsBadHeader(t *testing.T) {
	cases := []struct {
		name  string
		magic uint64
		ver   byte
	}{
		{"bad magic", runMagic ^ 0xFF, runVersion},
		{"bad version", runMagic, runVersion + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "r.run")
			var hdr [9]byte
			binary.LittleEndian.PutUint64(hdr[:8], tc.magic)
			hdr[8] = tc.ver
			require.NoError(t, os.WriteFile(path, hdr[:], 0o644))
			_, err := openRun(path)
			require.Error(t, err, "openRun must reject a bad header")
		})
	}
}

// TestRunFormatTruncatedRecord checks that a record cut short surfaces an error,
// not a clean EOF: the header is valid but the promised pairing bytes are absent.
func TestRunFormatTruncatedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.run")
	var hdr [9]byte
	binary.LittleEndian.PutUint64(hdr[:8], runMagic)
	hdr[8] = runVersion
	buf := append([]byte{}, hdr[:]...)
	buf = binary.AppendUvarint(buf, 5)  // K
	buf = binary.AppendUvarint(buf, 10) // promises |P| == 10 ...
	buf = append(buf, 'a', 'b')         // ... but only 2 bytes follow
	require.NoError(t, os.WriteFile(path, buf, 0o644))

	rr, err := openRun(path)
	require.NoError(t, err, "header is valid")
	_, _, err = rr.next()
	require.Error(t, err, "a truncated record must error")
	require.NotErrorIs(t, err, io.EOF, "truncation is not a clean end-of-stream")
	require.NoError(t, rr.close())
}

// TestExternalCancelledCleansUp: a run aborted mid-enumeration still removes its
// working directory (no leftover runs) when Preserve is unset.
func TestExternalCancelledCleansUp(t *testing.T) {
	dir := t.TempDir()
	fuse := &fuseCtx{Context: context.Background(), limit: 3}
	_, _, err := CountGroupExternal(fuse, 6, 0, 8, ExternalOptions{SpillBytes: xMedium, Dir: dir})
	require.ErrorIs(t, err, context.Canceled)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "an aborted run must leave no working subdirs")
}

// TestExternalDegenerateSpill drives SpillBytes below entryReserve so every new
// key becomes its own run (maximal spills and deep level reduction); the total
// still equals the in-memory count. One small 5×5 group keeps it cheap.
func TestExternalDegenerateSpill(t *testing.T) {
	const s, tt = 0, 6
	want, err := CountGroup(context.Background(), 5, s, tt)
	require.NoError(t, err)
	got, st, err := CountGroupExternal(context.Background(), 5, s, tt, ExternalOptions{SpillBytes: entryReserve - 1, Dir: t.TempDir()})
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Greater(t, st.Spills, 100, "every distinct key should spill")
}
