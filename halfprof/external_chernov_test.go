package halfprof

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExternalChernov6x6 runs the full external-path equivalence over every row
// of Chernov's 6×6 table: at thresholds {huge, medium, minimal} CountGroupExternal
// reproduces that row's published count bit-for-bit (specs/halfprof.md «внешний
// путь»). Rows run as parallel subtests; each threshold is a sub-subtest.
func TestExternalChernov6x6(t *testing.T) {
	rows := readChernov6x6(t)
	require.Len(t, rows, 45)
	for _, r := range rows {
		t.Run(rowName(r), func(t *testing.T) {
			t.Parallel()
			s, tt := r.y1*6+r.x1, r.y2*6+r.x2
			for _, spill := range []int{xHuge, xMedium, xMany} {
				got, _, err := CountGroupExternal(context.Background(), 6, s, tt, ExternalOptions{SpillBytes: spill, Dir: t.TempDir()})
				require.NoError(t, err)
				assert.Equalf(t, uint64(r.count), got, "row %+v at spill %d", r, spill)
			}
		})
	}
}

// TestExternalChernovBoardTotal pins the directed board total through the
// external aggregation path (workers-independent).
func TestExternalChernovBoardTotal(t *testing.T) {
	total, err := CountBoardExternal(context.Background(), 6, 8, ExternalOptions{SpillBytes: xMedium, Dir: t.TempDir()})
	require.NoError(t, err)
	assert.Equal(t, uint64(6637920), total)
}
