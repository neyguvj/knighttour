package split

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The oracle below is an independent directed backtracking enumerator written
// for the table tests of specs/split.md: it counts directed Hamiltonian paths
// (every start, every direction) and cycles (fixed start, both directions —
// exactly the directed convention of docs/requirements.md). The engine counts
// edge sets, so the comparison is Count×2 against the oracle.

// knightAdj builds the n×n knight adjacency from first principles (row-major
// ids), independent of the production graph package.
func knightAdj(n int) [][]int {
	adj := make([][]int, n*n)
	for v := range n * n {
		r, c := v/n, v%n
		for _, m := range [][2]int{{-2, -1}, {-2, 1}, {-1, -2}, {-1, 2}, {1, -2}, {1, 2}, {2, -1}, {2, 1}} {
			nr, nc := r+m[0], c+m[1]
			if nr >= 0 && nr < n && nc >= 0 && nc < n {
				adj[v] = append(adj[v], nr*n+nc)
			}
		}
	}
	return adj
}

// oracleTours counts directed Hamiltonian paths (paths=true) or directed
// Hamiltonian cycles (cycles through the fixed vertex 0, both directions) on
// the n×n knight board. A dead-unvisited-vertex prune keeps the search short.
func oracleTours(n int, paths bool) uint64 {
	adj := knightAdj(n)
	total := len(adj)
	var seen [64]bool

	// live reports a vertex that is unvisited and has no unvisited neighbour:
	// unless it is the very last cell to join, the tour can never complete.
	live := func(last int) bool {
		var stranded []int
		for v := range total {
			if seen[v] || v == last {
				continue
			}
			free := false
			for _, w := range adj[v] {
				if !seen[w] {
					free = true
					break
				}
			}
			if !free {
				stranded = append(stranded, v)
			}
		}
		return len(stranded) <= 1
	}

	var walk func(v, depth int) uint64
	walk = func(v, depth int) uint64 {
		if depth == total {
			if paths {
				return 1
			}
			if slices.Contains(adj[v], 0) {
				return 1
			}
			return 0
		}
		var cnt uint64
		for _, w := range adj[v] {
			if seen[w] || (!paths && w == 0) {
				continue
			}
			seen[w] = true
			if live(w) {
				cnt += walk(w, depth+1)
			}
			seen[w] = false
		}
		return cnt
	}

	if !paths {
		seen[0] = true
		return walk(0, 1)
	}
	var cnt uint64
	for s := range total {
		seen[s] = true
		cnt += walk(s, 1)
		seen[s] = false
	}
	return cnt
}

// TestOracleVsDFS pins the engine against the independent enumerator on small
// boards (table tests of specs/split.md): Count×2 equals the directed oracle.
func TestOracleVsDFS(t *testing.T) {
	cases := []struct {
		n     int
		fam   Family
		paths bool
	}{
		{2, Paths, true}, {2, Cycles, false},
		{3, Paths, true}, {3, Cycles, false},
		{4, Paths, true}, {4, Cycles, false},
		{5, Paths, true}, {5, Cycles, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dx%d-%s", tc.n, tc.n, famName(tc.fam)), func(t *testing.T) {
			total, err := Count(tc.n, tc.fam)
			require.NoError(t, err)
			assert.Equal(t, oracleTours(tc.n, tc.paths), 2*total, "edge-set count ×2 must equal the directed oracle")
		})
	}
}

// TestReferenceCounts pins docs/requirements.md: Paths×2 = 1728 (5×5), Cycles×2
// = 0 (5×5) and Cycles×2 = 19 724 (6×6, both variants). The mirror rows also
// carry the variant identity of specs/split.md: Mirror ≡ Straight on even n and
// |Σ| of the halves coincide (L/R symmetry), so every point checks its census.
func TestReferenceCounts(t *testing.T) {
	cases := []struct {
		n            int
		fam          Family
		v            Variant
		wantDirected uint64
	}{
		{5, Paths, Straight, 1_728},
		{5, Cycles, Straight, 0},
		{6, Cycles, Straight, 19_724},
		{6, Cycles, Mirror, 19_724},
	}
	results := make(map[string]Census, len(cases))
	for _, tc := range cases {
		name := fmt.Sprintf("%dx%d-%s-%s", tc.n, tc.n, famName(tc.fam), variantName(tc.v))
		t.Run(name, func(t *testing.T) {
			total, cen, err := CountWithCensus(tc.n, tc.fam, tc.v)
			require.NoError(t, err)
			assert.Equal(t, tc.wantDirected, 2*total)
			assert.Positive(t, cen.LayerPeak, "the DP must have run layers")
			if tc.n%2 == 0 {
				assert.Equal(t, cen.SigmaL, cen.SigmaR, "even board: |Σ_L| = |Σ_R|")
			}
			results[name] = cen
		})
	}
	t.Run("mirror-equals-straight", func(t *testing.T) {
		s := results["6x6-cycles-straight"]
		m := results["6x6-cycles-mirror"]
		assert.Equal(t, s.LayerPeak, m.LayerPeak, "mirror builds one half: same layer peak")
	})
}

// TestDeterminism runs the same input twice and compares the total and every
// Census field bit-exactly (wall time is the only float).
func TestDeterminism(t *testing.T) {
	total1, cen1, err := CountWithCensus(5, Paths, Straight)
	require.NoError(t, err)
	total2, cen2, err := CountWithCensus(5, Paths, Straight)
	require.NoError(t, err)
	assert.Equal(t, total1, total2)
	assert.Equal(t, cen1, cen2)
}

// TestOverCap lowers the internal lever just above the initial tables and pins
// that the run aborts with ErrOverCap carrying progress counters instead of OOM; a
// normal run afterwards proves the process is alive.
func TestOverCap(t *testing.T) {
	const low = 1 << 20 // comfortably below the first doubling of 5×5 tables
	restore := setSoftCapForTest(low)
	defer restore()

	total, err := Count(5, Paths)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOverCap)
	assert.Zero(t, total)

	var over *OverCapError
	require.ErrorAs(t, err, &over)
	assert.Positive(t, over.Bytes, "cutoff must report reserved bytes")
	assert.Greater(t, over.Bytes, over.Cap)
	assert.Equal(t, low, over.Cap, "Cap is the lever snapshot at abort")

	restored := setSoftCapForTest(DefaultSoftCapBytes)
	defer restored()
	got, err := Count(5, Cycles)
	require.NoError(t, err)
	assert.Zero(t, got)
}

// TestValidation pins the error branches: unknown family/variant, unusable n
// and the mirror variant on odd boards.
func TestValidation(t *testing.T) {
	_, err := Count(5, Family(99))
	require.Error(t, err)
	_, _, err = CountWithCensus(5, Paths, Variant(7))
	require.Error(t, err)
	for _, n := range []int{0, 1, 9} {
		_, e := Count(n, Paths)
		require.Errorf(t, e, "n=%d must be rejected", n)
	}
	_, _, err = CountWithCensus(5, Paths, Mirror)
	require.Error(t, err, "mirror needs an even board")
}

// TestGeometry pins the cut invariants of specs/split.md: the seam of a
// balanced cut holds 6n−8 edges for n ∈ [5,8]; per-side stub numbering is a
// bijection of the seam ends; and the mirror permutation of even boards maps
// the seam to itself involutively.
func TestGeometry(t *testing.T) {
	for _, n := range []int{5, 6, 7, 8} {
		g := newCutGeom(n)
		t.Run(fmt.Sprintf("seam-%d", n), func(t *testing.T) {
			assert.Len(t, g.seam, 6*n-8)

			seenL := make([]int, len(g.seam))
			seenR := make([]int, len(g.seam))
			for v, stubs := range g.perCell {
				for _, e := range stubs {
					if v%g.n < g.cutCol {
						seenL[e]++
					} else {
						seenR[e]++
					}
				}
			}
			for i := range seenL {
				assert.Equal(t, 1, seenL[i], "seam edge %d left end", i)
				assert.Equal(t, 1, seenR[i], "seam edge %d right end", i)
			}

			if n%2 == 0 {
				for i := range g.seam {
					assert.Equal(t, i, g.mirror[g.mirror[i]], "mirror involution at %d", i)
				}
			} else {
				assert.Nil(t, g.mirror)
			}
		})
	}
}

// setSoftCapForTest moves the internal byte lever and returns its restore.
func setSoftCapForTest(v int) (restore func()) {
	old := softCap
	softCap = v
	return func() { softCap = old }
}

// famName renders a family as its test-name segment.
func famName(fam Family) string {
	if fam == Cycles {
		return "cycles"
	}
	return "paths"
}

// variantName renders a variant as its test-name segment.
func variantName(v Variant) string {
	if v == Mirror {
		return "mirror"
	}
	return "straight"
}
