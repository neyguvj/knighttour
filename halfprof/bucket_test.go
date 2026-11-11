package halfprof

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBucketEquivalence5x5 pins that bucketing changes nothing: for every
// non-zero endpoint orbit of 5×5 and passes ∈ {1,2,3,8}, CountGroupWithPasses
// equals the single-pass count (specs/halfprof.md «bucket» invariants).
func TestBucketEquivalence5x5(t *testing.T) {
	for _, passes := range []int{1, 2, 3, 8} {
		t.Run("passes"+strconv.Itoa(passes), func(t *testing.T) {
			for _, gr := range Groups(5) {
				base, err := CountGroup(context.Background(), 5, gr.S, gr.T)
				require.NoError(t, err)
				if base == 0 {
					continue // zero orbits carry no join to reproduce
				}
				got, _, err := CountGroupWithPasses(context.Background(), 5, gr.S, gr.T, passes)
				require.NoError(t, err)
				assert.Equalf(t, base, got, "group (%d,%d) at passes %d", gr.S, gr.T, passes)
			}
		})
	}
}

// TestBucketEquivalenceChernov6x6 runs the same equivalence over every row of
// Chernov's 6×6 table: each pass count reproduces that row's published count.
func TestBucketEquivalenceChernov6x6(t *testing.T) {
	for _, r := range readChernov6x6(t) {
		t.Run(rowName(r), func(t *testing.T) {
			t.Parallel()
			s, tt := r.y1*6+r.x1, r.y2*6+r.x2
			for _, passes := range []int{1, 2, 3, 8} {
				got, _, err := CountGroupWithPasses(context.Background(), 6, s, tt, passes)
				require.NoError(t, err)
				assert.Equalf(t, uint64(r.count), got, "row %+v at passes %d", r, passes)
			}
		})
	}
}

// TestBucketDeterminism: two identical multi-pass runs agree bit-for-bit.
func TestBucketDeterminism(t *testing.T) {
	first, c1, err := CountGroupWithPasses(context.Background(), 6, 0, 8, 3)
	require.NoError(t, err)
	second, c2, err := CountGroupWithPasses(context.Background(), 6, 0, 8, 3)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, c1, c2)
}

// TestBucketMidPassCancellation: a ctx cancelled a few polls into an 8-pass run
// must abort the whole count with context.Canceled, not return a partial sum.
// The fuse trips on the N-th Err call; this board enumerates millions of nodes,
// so its dense per-tick polls guarantee the trip lands inside pass 0's descent
// — i.e. mid-count through the multi-pass path (specs/halfprof.md).
func TestBucketMidPassCancellation(t *testing.T) {
	const fuseLimit = 3
	fuse := &fuseCtx{Context: context.Background(), limit: fuseLimit}
	_, _, err := CountGroupWithPasses(fuse, 6, 0, 8, 8)
	require.ErrorIs(t, err, context.Canceled)
	assert.Greater(t, fuse.calls, fuseLimit, "cancellation must land after work started")
}

// TestBucketOverCapHint: with a token cap the multi-pass count fails on a full
// pass; the error still identifies ErrOverCap / *OverCapError and names the
// remedy (raise passes).
func TestBucketOverCapHint(t *testing.T) {
	old := softCap
	softCap = 1024
	defer func() { softCap = old }()

	_, _, err := CountGroupWithPasses(context.Background(), 6, 0, 8, 1)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOverCap)
	var oe *OverCapError
	require.ErrorAs(t, err, &oe)
	assert.Contains(t, err.Error(), "raise passes")
}

// TestBucketInvalidPasses: passes below 1 is a caller error.
func TestBucketInvalidPasses(t *testing.T) {
	_, _, err := CountGroupWithPasses(context.Background(), 5, 0, 6, 0)
	require.Error(t, err)
}

// TestBucketBoardPin: board-level composition with passes=3 reproduces the
// directed 5×5 reference exactly.
func TestBucketBoardPin(t *testing.T) {
	got, err := CountBoardWithPasses(context.Background(), 5, 2, 3)
	require.NoError(t, err)
	assert.Equal(t, uint64(1728), got)
}
