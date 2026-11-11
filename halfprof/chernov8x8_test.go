package halfprof

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chernov8x8Undirected is the published undirected 8×8 total: Σ k·count over
// the endpoint-pair orbits of the reference table (directed total = 2×).
const chernov8x8Undirected uint64 = 9_795_914_085_489_952

// readChernovTable parses a Chernov endpoint-pair CSV (comment lines start #,
// columns y1,x1,y2,x2,k,count) shared by the 6×6 and 8×8 oracles.
func readChernovTable(t *testing.T, path string) [][6]int {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var out [][6]int
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ",")
		require.Len(t, f, 6, "line %q", line)
		var v [6]int
		for i, s := range f {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			require.NoError(t, err, "line %q", line)
			v[i] = n
		}
		out = append(out, v)
	}
	return out
}

// TestChernov8x8TableFormat pins the 8×8 reference snapshot as a format-and-sum
// oracle only: 136 differently-colored orbit rows, k in {1,2,4,8}, and
// Σ k·count equal to the published undirected total. It runs no enumeration —
// counting 8×8 groups is wave-4 bench work, not make check.
func TestChernov8x8TableFormat(t *testing.T) {
	rows := readChernovTable(t, "testdata/chernov8x8.csv")
	require.Len(t, rows, 136)
	var sum uint64
	for _, r := range rows {
		y1, x1, y2, x2, k, count := r[0], r[1], r[2], r[3], r[4], r[5]
		assert.Less(t, max(y1, x1, y2, x2), 8, "row %v", r)
		assert.Contains(t, []int{1, 2, 4, 8}, k, "row %v", r)
		// Endpoints of a Hamiltonian path on an even board lie on opposite colors.
		assert.NotEqual(t, (y1+x1)%2, (y2+x2)%2, "row %v", r)
		sum += uint64(k) * uint64(count)
	}
	assert.Equal(t, chernov8x8Undirected, sum)
}
