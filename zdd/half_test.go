package zdd

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sigmaOf encodes one terminal-matching description into the canonical Sigma:
// pairs of terminal ids (termGlobal for a global end), sorted by pair word.
func sigmaOf(pairs [][2]uint8) Sigma {
	var sg Sigma
	words := make([]uint16, 0, len(pairs))
	for _, p := range pairs {
		a, b := p[0], p[1]
		if a > b {
			a, b = b, a
		}
		words = append(words, uint16(a)<<6|uint16(b))
		if a != termGlobal {
			sg.sMask |= 1 << a
		} else {
			sg.ends++
		}
		if b != termGlobal {
			sg.sMask |= 1 << b
		} else {
			sg.ends++
		}
	}
	slices.Sort(words)
	for i, w := range words {
		sg.words[i/5] |= uint64(w) << (12 * (i % 5))
	}
	return sg
}

// bruteHalf enumerates every edge subset of a half graph and keeps those that
// form a valid fragment cover, deriving σ directly from the formalism (each
// connected component is a path whose two ends are terminals: taken stubs or
// degree-1 closures = global ends). It is the independent oracle for Sigmas().
func bruteHalf(nVerts int, edges [][2]int, stubOf []int32, fam Family) map[Sigma]uint64 {
	out := make(map[Sigma]uint64)
	for sub := range 1 << len(edges) {
		if sg, ok := halfSetSigma(nVerts, edges, stubOf, fam, sub); ok {
			out[sg]++
		}
	}
	return out
}

// halfSetSigma classifies one edge subset; ok is false when the subset leaves
// a vertex uncovered or over-full, closes a cycle, breaks the two-terminal
// fragment invariant, or exceeds ε.
func halfSetSigma(nVerts int, edges [][2]int, stubOf []int32, fam Family, sub int) (Sigma, bool) {
	deg := make([]int, nVerts)
	uf := make([]int, nVerts)
	for v := range uf {
		uf[v] = v
	}
	stubTerms := make([][]uint8, nVerts) // per real vertex: taken stub terminals
	var gCount int

	find := func(v int) int {
		for uf[v] != v {
			uf[v] = uf[uf[v]]
			v = uf[v]
		}
		return v
	}
	union := func(a, b int) bool {
		ra, rb := find(a), find(b)
		if ra == rb {
			return false // cycle inside the half: forbidden fragment
		}
		uf[rb] = ra
		return true
	}

	for k := range len(edges) {
		if sub&(1<<k) == 0 {
			continue
		}
		e := edges[k]
		if s := stubOf[k]; s >= 0 {
			r := e[0] // exactly one real end (validated input); find it
			if r >= nVerts {
				r = e[1]
			}
			deg[r]++
			stubTerms[r] = append(stubTerms[r], uint8(s))
			continue
		}
		deg[e[0]]++
		deg[e[1]]++
		if !union(e[0], e[1]) {
			return Sigma{}, false
		}
	}

	// Degrees: Paths cover with ≤ 2, Cycles demand exactly 2.
	for v := range nVerts {
		if deg[v] == 0 || deg[v] > 2 {
			return Sigma{}, false
		}
		if fam == Cycles && deg[v] != 2 {
			return Sigma{}, false
		}
	}

	// Per component: stub terminals plus degree-1 closures (global ends) must
	// be exactly two — the fragment's pair.
	for v := range nVerts {
		if deg[v] == 1 {
			stubTerms[v] = append(stubTerms[v], termGlobal)
			gCount++
		}
	}
	var pairs [][2]uint8
	seen := make(map[int]bool, nVerts)
	for v := range nVerts {
		r := find(v)
		if seen[r] {
			continue
		}
		seen[r] = true
		var t []uint8
		for u := range nVerts {
			if find(u) == r {
				t = append(t, stubTerms[u]...)
			}
		}
		if len(t) != 2 {
			return Sigma{}, false // dangling end or cycle with no terminals
		}
		pairs = append(pairs, [2]uint8{t[0], t[1]})
	}
	if fam == Cycles && gCount > 0 {
		return Sigma{}, false
	}
	if gCount > 2 {
		return Sigma{}, false
	}
	return sigmaOf(pairs), true
}

// tinyHalfG is one small open-seam graph for the oracle table.
type tinyHalfG struct {
	edges   [][2]int
	stubOf  []int32
	edgeIDs []int32
	nVerts  int
}

// tinyHalfGraphs returns hand-built halves ≤ 8 edges for the oracle sweep
// with their subtest names (kept out of the struct so its layout stays
// fieldalignment-clean).
func tinyHalfGraphs() ([]tinyHalfG, []string) {
	graphs := []tinyHalfG{
		{
			nVerts:  2,
			edges:   [][2]int{{0, 1}, {0, 2}, {1, 3}},
			stubOf:  []int32{-1, 0, 1},
			edgeIDs: []int32{0, 1, 2},
		},
		{
			nVerts:  2,
			edges:   [][2]int{{0, 1}, {0, 2}, {0, 3}},
			stubOf:  []int32{-1, 0, 1},
			edgeIDs: []int32{0, 1, 2},
		},
		{
			nVerts:  1,
			edges:   [][2]int{{0, 1}, {0, 2}},
			stubOf:  []int32{0, 1},
			edgeIDs: []int32{0, 1},
		},
		{
			nVerts:  3,
			edges:   [][2]int{{0, 1}, {1, 2}, {1, 3}},
			stubOf:  []int32{-1, -1, 0},
			edgeIDs: []int32{0, 1, 2},
		},
		{
			nVerts:  3,
			edges:   [][2]int{{0, 1}, {1, 2}, {0, 2}, {0, 3}, {1, 4}},
			stubOf:  []int32{-1, -1, -1, 0, 1},
			edgeIDs: []int32{0, 1, 2, 3, 4},
		},
	}
	names := []string{
		"path-stub-stub", "path-stubs-together", "isolated-two-stubs",
		"three-path-mid-stub", "triangle-stubs",
	}
	return graphs, names
}

// TestHalfSigmasMatchOracle pins Sigmas() against the independent subset
// enumeration for both families on every tiny half graph.
func TestHalfSigmasMatchOracle(t *testing.T) {
	for _, fam := range [2]Family{Paths, Cycles} {
		graphs, names := tinyHalfGraphs()
		for gi, g := range graphs {
			t.Run(fmt.Sprintf("%s/%d", names[gi], fam), func(t *testing.T) {
				s := NewHalfSession(fam)
				order := OrderNarrow(g.nVerts+len(g.stubOf), g.edges)
				hd, err := s.BuildHalf(g.nVerts, g.edges, g.stubOf, g.edgeIDs, order)
				require.NoError(t, err)
				want := bruteHalf(g.nVerts, g.edges, g.stubOf, fam)
				got, err := hd.Sigmas()
				require.NoError(t, err)
				assert.Equal(t, want, got)
			})
		}
	}
}

// TestHalfGlue5x5 pins the 5×5 reference through BuildHalf + Sigmas + Glue:
// Paths × 2 = 1 728 and Cycles × 2 = 0 (docs/requirements.md).
func TestHalfGlue5x5(t *testing.T) {
	cases := []struct {
		fam   Family
		want  uint64 // edge sets (directed tours = 2×)
		joins uint64 // Cycles: no S group is shared by both halves (ADR-043)
	}{
		{Paths, 864, 0}, // joins > 0 checked below for Paths only
		{Cycles, 0, 0},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("family%d", tc.fam), func(t *testing.T) {
			lg, rg, _ := boardHalves(5)
			s := NewHalfSession(tc.fam)
			left := lg.buildHalf(s)
			right := rg.buildHalf(s)
			lSig, errL := left.Sigmas()
			rSig, errR := right.Sigmas()
			require.NoError(t, errL)
			require.NoError(t, errR)
			total, joins := Glue(lSig, rSig, tc.fam)
			assert.Equal(t, tc.want, total)
			if tc.fam == Paths {
				assert.Positive(t, joins)
			} else {
				assert.Zero(t, joins) // 5×5 Cycles: disjoint S groups (ADR-043 table)
			}
		})
	}
}

// TestHalfSigmaSplitAnchor5x5 cross-checks the σ formalism against the split
// engine's measured anchor (ADR-043): 5×5 Paths left half |Σ_L| = 856 821.
func TestHalfSigmaSplitAnchor5x5(t *testing.T) {
	lg, _, _ := boardHalves(5)
	s := NewHalfSession(Paths)
	left := lg.buildHalf(s)
	sig, err := left.Sigmas()
	require.NoError(t, err)
	assert.Len(t, sig, 856_821)
}

// TestHalfDeterministic runs two independent sessions on 5×5 Paths and pins
// nodes, level peaks and the full σ tables (and hence Glue) bit-for-bit.
func TestHalfDeterministic(t *testing.T) {
	lg, rg, _ := boardHalves(5)
	run := func() (int, int, map[Sigma]uint64, uint64) {
		s := NewHalfSession(Paths)
		left := lg.buildHalf(s)
		right := rg.buildHalf(s)
		lSig, err := left.Sigmas()
		require.NoError(t, err)
		rSig, err := right.Sigmas()
		require.NoError(t, err)
		total, _ := Glue(lSig, rSig, Paths)
		return left.LiveNodes(), left.LevelPeak(), lSig, total
	}
	// The session registry covers arena + unique table + both half memos.
	sess := NewHalfSession(Paths)
	lh := lg.buildHalf(sess)
	rh := rg.buildHalf(sess)
	assert.Positive(t, rh.registryBytes())
	assert.Equal(t, rh.registryBytes(), lh.registryBytes())

	n1, p1, s1, tot1 := run()
	n2, p2, s2, tot2 := run()
	assert.Equal(t, n1, n2)
	assert.Equal(t, p1, p2)
	assert.Equal(t, s1, s2)
	assert.Equal(t, tot1, tot2)
}

// TestHalfMirrorIdentity6x6Cycles pins the variant identity of specs/zdd.md:
// the σ table of one build reflected by ρ glues to the same total as the two
// straight builds (the 6×6 Cycles board, small enough for a unit test).
func TestHalfMirrorIdentity6x6Cycles(t *testing.T) {
	gl, gr, seam := boardHalves(6)
	perm := mirrorPerm(6, seam)
	assert.NotNil(t, perm)

	straight := NewHalfSession(Cycles)
	left := gl.buildHalf(straight)
	right := gr.buildHalf(straight)
	lSig, errL := left.Sigmas()
	rSig, errR := right.Sigmas()
	require.NoError(t, errL)
	require.NoError(t, errR)
	totalStraight, _ := Glue(lSig, rSig, Cycles)

	mirror := NewHalfSession(Cycles)
	ml := gl.buildHalf(mirror)
	mSig, errM := ml.Sigmas()
	require.NoError(t, errM)
	totalMirror, _ := Glue(mSig, reflectSigmas(mSig, perm), Cycles)

	assert.Equal(t, uint64(19_724/2), totalStraight) // edge sets of the directed oracle
	assert.Equal(t, totalStraight, totalMirror)
}

// TestBuildHalfValidation pins the open-seam input table of specs/zdd.md:
// every malformed BuildHalf input yields an error — no panics and no
// unbounded allocation behind it.
func TestBuildHalfValidation(t *testing.T) {
	cases := []struct {
		name    string
		wantErr string
		edges   [][2]int
		stubOf  []int32
		edgeIDs []int32
		fam     Family
		nVerts  int
	}{
		{name: "unknown family", wantErr: "unknown family", fam: Family(7), nVerts: 2,
			edges: [][2]int{{0, 1}}, stubOf: []int32{-1}, edgeIDs: []int32{0}},
		{name: "doubled seam stub", wantErr: "doubled seam stub", nVerts: 2,
			edges: [][2]int{{0, 1}, {0, 2}, {1, 3}}, stubOf: []int32{-1, 0, 0}, edgeIDs: []int32{0, 1, 2}},
		{name: "repeated edge id", wantErr: "edgeIds repeat", nVerts: 2,
			edges: [][2]int{{0, 1}, {0, 2}, {1, 3}}, stubOf: []int32{-1, 0, 1}, edgeIDs: []int32{0, 1, 1}},
		{name: "negative edge id", wantErr: "outside", nVerts: 2,
			edges: [][2]int{{0, 1}, {0, 2}, {1, 3}}, stubOf: []int32{-1, 0, 1}, edgeIDs: []int32{0, -2, 2}},
		{name: "oversized edge id", wantErr: "outside", nVerts: 2,
			edges: [][2]int{{0, 1}, {0, 2}, {1, 3}}, stubOf: []int32{-1, 0, 1}, edgeIDs: []int32{0, maxHalfEdges, 2}},
		{name: "stub without real end", wantErr: "one real end", nVerts: 1,
			edges: [][2]int{{3, 4}}, stubOf: []int32{0}, edgeIDs: []int32{0}},
		{name: "internal edge with virtual end", wantErr: "virtual vertex", nVerts: 2,
			edges: [][2]int{{0, 3}}, stubOf: []int32{-1}, edgeIDs: []int32{0}},
		{name: "stubOf length mismatch", wantErr: "lengths", nVerts: 2,
			edges: [][2]int{{0, 1}, {0, 2}, {1, 3}}, stubOf: []int32{-1, 0}, edgeIDs: []int32{0, 1, 2}},
		{name: "edgeIds length mismatch", wantErr: "lengths", nVerts: 2,
			edges: [][2]int{{0, 1}, {0, 2}, {1, 3}}, stubOf: []int32{-1, 0, 1}, edgeIDs: []int32{0, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewHalfSession(tc.fam)
			_, err := s.BuildHalf(tc.nVerts, tc.edges, tc.stubOf, tc.edgeIDs, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestHalfSigmasOverCap cuts the σ pass at a ceiling its own build fits
// under (specs/zdd.md «Мягкий потолок»): Sigmas returns ErrOverCap with the
// session closed and caches the cutoff; further builds are refused.
func TestHalfSigmasOverCap(t *testing.T) {
	lg, _, _ := boardHalves(5)
	base := NewHalfSession(Paths)
	hd0 := lg.buildHalf(base) // default ceiling: the reference registry size
	registry := hd0.registryBytes()

	old := arenaCap
	arenaCap = int(registry + 32<<20) // fits the build, not the ~60 MB σ table
	defer func() { arenaCap = old }()

	s := NewHalfSession(Paths)
	hd, err := s.BuildHalf(lg.nVerts, lg.edges, lg.stubOf, lg.edgeIDs, lg.narrowOrder())
	require.NoError(t, err)
	_, err = hd.Sigmas()
	require.ErrorIs(t, err, ErrOverCap)
	var oc *OverCapError
	require.ErrorAs(t, err, &oc)
	assert.Greater(t, oc.Bytes, oc.Cap)

	_, err2 := hd.Sigmas()
	assert.Equal(t, err, err2, "the cutoff is cached")

	_, err = s.BuildHalf(lg.nVerts, lg.edges, lg.stubOf, lg.edgeIDs, lg.narrowOrder())
	require.Error(t, err, "a session cut over cap refuses further builds")
}

// TestHalfOverCap aborts a 5×5 half build at a lowered ceiling: ErrOverCap
// with progress counters and the cap snapshot; the session then refuses work.
func TestHalfOverCap(t *testing.T) {
	old := arenaCap
	arenaCap = 1 << 20
	defer func() { arenaCap = old }()

	lg, _, _ := boardHalves(5)
	s := NewHalfSession(Paths)
	_, err := lg.buildHalfErr(s)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOverCap)
	var oc *OverCapError
	require.ErrorAs(t, err, &oc)
	assert.Positive(t, oc.Nodes)
	assert.Greater(t, oc.Bytes, oc.Cap)

	_, err = lg.buildHalfErr(s)
	assert.Error(t, err, "a session that hit the cap must refuse further builds")
}

// TestSeamKeyRoundTrip repacks every memo key of a tiny half build: the σ
// replay decodes states from these bytes, so pack/unpack must be exact.
func TestSeamKeyRoundTrip(t *testing.T) {
	graphs, _ := tinyHalfGraphs()
	g := graphs[0]
	s := NewHalfSession(Paths)
	order := OrderNarrow(g.nVerts+len(g.stubOf), g.edges)
	hd, err := s.BuildHalf(g.nVerts, g.edges, g.stubOf, g.edgeIDs, order)
	require.NoError(t, err)
	checked := 0
	for i := range hd.m {
		hd.b.memo.levels[i].forEach(func(_ uint32, key []byte) {
			st := unpackKeySeam(key)
			assert.Equal(t, key, packKey(&st, make([]byte, 0, len(key))))
			checked++
		})
	}
	assert.Positive(t, checked)
}

// buildHalfErr is buildHalf with the error surfaced (over-cap tests).
func (g halfGeom) buildHalfErr(s *HalfSession) (*HalfDiagram, error) {
	order := OrderNarrow(g.nTotal(), g.edges)
	return s.BuildHalf(g.nVerts, g.edges, g.stubOf, g.edgeIDs, order)
}

// TestHalfGlueRejectsInconsistentSigma guards the Glue marker check: a hand
// built σ whose ε disagrees with its pair words never contributes.
func TestHalfGlueRejectsInconsistentSigma(t *testing.T) {
	l := map[Sigma]uint64{sigmaOf([][2]uint8{{0, termGlobal}}): 5} // S={0}, ε=1
	r := map[Sigma]uint64{{sMask: 1, ends: 2}: 7}                  // same S, no pairs, ε=2
	total, joins := Glue(l, r, Paths)
	assert.Zero(t, total)
	assert.EqualValues(t, 1, joins)
}
