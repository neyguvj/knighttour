package halfprof

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chernov6x6Row is one line of the reference table: endpoints as (y,x) pairs,
// D4 orbit size k and the undirected tour count between them.
type chernov6x6Row struct{ y1, x1, y2, x2, k, count int }

// readChernov6x6 parses the embedded reference table via the shared CSV reader.
func readChernov6x6(t *testing.T) []chernov6x6Row {
	t.Helper()
	raw := readChernovTable(t, "testdata/chernov6x6.csv")
	out := make([]chernov6x6Row, 0, len(raw))
	for _, v := range raw {
		out = append(out, chernov6x6Row{v[0], v[1], v[2], v[3], v[4], v[5]})
	}
	return out
}

// TestChernov6x6Table is the primary oracle: every row of A. Chernov's 6×6
// endpoint-pair table (alex-black.ru, 2014) must be reproduced exactly, and
// Σ k·count must give the published undirected total 3 318 960 (directed
// 6 637 920). Rows run as parallel subtests.
func TestChernov6x6Table(t *testing.T) {
	rows := readChernov6x6(t)
	require.Len(t, rows, 45)
	for _, r := range rows {
		t.Run(rowName(r), func(t *testing.T) {
			t.Parallel()
			s, tt := r.y1*6+r.x1, r.y2*6+r.x2
			got, err := CountGroup(context.Background(), 6, s, tt)
			require.NoError(t, err)
			assert.Equal(t, uint64(r.count), got)
		})
	}
	var sum uint64
	for _, r := range rows {
		sum += uint64(r.k) * uint64(r.count)
	}
	assert.Equal(t, uint64(3318960), sum)
}

// TestChernov6x6BoardTotal pins the full directed board total through the
// public aggregation path.
func TestChernov6x6BoardTotal(t *testing.T) {
	total, err := CountBoard(context.Background(), 6, 8)
	require.NoError(t, err)
	assert.Equal(t, uint64(6637920), total)
}

// rowName renders a subtest label from the (y,x) endpoint coordinates.
func rowName(r chernov6x6Row) string {
	return "p" + strconv.Itoa(r.y1) + strconv.Itoa(r.x1) + "-" + strconv.Itoa(r.y2) + strconv.Itoa(r.x2)
}
