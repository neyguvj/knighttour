package symmetry

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/state"
)

// pairKey identifies a transformed pair (mask, end) for brute-force orbit sets.
type pairKey struct {
	st  state.State
	end uint8
}

// bruteClass computes the reference canonical class: the lexicographic minimum
// of (t(st), t(end)) over D4 and the number of distinct such pairs (specs/symmetry.md).
func bruteClass(s *Symmetry, st state.State, end int) (state.State, uint8, int) {
	bestSt, bestEnd := s.transformState(0, st), uint8(s.TransformCell(0, end))
	seen := make(map[pairKey]bool)
	for t := range numTransforms {
		imgSt := s.transformState(uint8(t), st)
		imgEnd := uint8(s.TransformCell(uint8(t), end))
		if imgSt < bestSt || (imgSt == bestSt && imgEnd < bestEnd) {
			bestSt, bestEnd = imgSt, imgEnd
		}
		seen[pairKey{imgSt, imgEnd}] = true
	}
	return bestSt, bestEnd, len(seen)
}

// bruteStab returns {t : t(K) == K} by transforming K through all eight perms.
func bruteStab(s *Symmetry, K state.State) uint8 {
	var stab uint8
	for t := range numTransforms {
		if s.transformState(uint8(t), K) == K {
			stab |= 1 << t
		}
	}
	return stab
}

// randomPair is a deterministic test input: a mask with its end inside it.
type randomPair struct {
	st  state.State
	end int
}

// randomPairs draws count random (mask, end) pairs with end ∈ mask from a
// fixed seed across masks of every non-empty size class.
func randomPairs(s *Symmetry, seed int64, count int) []randomPair {
	rnd := rand.New(rand.NewSource(seed))
	total := s.size * s.size
	out := make([]randomPair, 0, count)
	for range count {
		bits := 1 + rnd.Intn(total)
		var st state.State
		for range bits {
			st = st.Visit(rnd.Intn(total))
		}
		var end int
		for u := range st.AllVisited() {
			end = u
		}
		out = append(out, randomPair{st, end})
	}
	return out
}

// CanonicalMaskFrame: K is the brute-force minimum image, frame its smallest
// argmin, stab matches brute-force Stab(K) and is a subgroup, and every
// minimizing frame yields the same downstream class (specs/symmetry.md).
func TestCanonicalMaskFrame(t *testing.T) {
	for _, size := range []int{5, 6, 7, 8} {
		t.Run(fmt.Sprintf("%dx%d", size, size), func(t *testing.T) {
			s := NewSymmetry(size)
			for _, p := range randomPairs(s, int64(size), 60) {
				K, frame, stab := s.CanonicalMaskFrame(p.st)

				wantK, _, _ := bruteClass(s, p.st, p.end)
				assert.Equal(t, wantK, K, "K is the mask minimum")
				require.Equal(t, s.transformState(frame, p.st), K, "frame reaches K")

				for f := range frame {
					assert.NotEqual(t, K, s.transformState(f, p.st), "frame is the smallest argmin")
				}

				assert.Equal(t, bruteStab(s, K), stab, "stab matches brute force")
				assertMaskIsSubgroup(t, s, stab)
			}
		})
	}
}

// assertMaskIsSubgroup checks stab contains the identity and is closed under
// the compose LUT.
func assertMaskIsSubgroup(t *testing.T, s *Symmetry, stab uint8) {
	t.Helper()
	assert.NotZero(t, stab&(1<<0), "identity fixes every mask")
	for i := range numTransforms {
		for j := range numTransforms {
			if stab&(1<<i) != 0 && stab&(1<<j) != 0 {
				assert.NotZero(t, stab&(1<<s.compose[i][j]), "closed: %d∘%d", i, j)
			}
		}
	}
}

// Downstream results (ClassRep / CellOrbitSize of the transformed end) are
// identical for every minimizing frame — the tie-break is structurally gone.
func TestCanonicalMaskFrameArgminIndependence(t *testing.T) {
	s := NewSymmetry(5)
	for _, p := range randomPairs(s, 1801, 200) {
		K, _, stab := s.CanonicalMaskFrame(p.st)
		wantRep, wantSize := uint8(0), 0
		first := true
		for f := range numTransforms {
			if s.transformState(uint8(f), p.st) != K {
				continue
			}
			c := s.TransformCell(uint8(f), p.end)
			rep, size := s.ClassRep(stab, c), s.CellOrbitSize(stab, c)
			if first {
				wantRep, wantSize, first = rep, size, false
				continue
			}
			assert.Equal(t, wantRep, rep, "frame %d rep", f)
			assert.Equal(t, wantSize, size, "frame %d orbitSize", f)
		}
	}
}

// ClassRep: idempotent, equals the minimum of the stab-orbit, agrees exactly
// with brute-force stab-relatedness (same rep ⟺ related).
func TestClassRep(t *testing.T) {
	s := NewSymmetry(5)
	for _, p := range randomPairs(s, 1802, 120) {
		_, _, stab := s.CanonicalMaskFrame(p.st)
		rep := s.ClassRep(stab, p.end)

		assert.Equal(t, rep, s.ClassRep(stab, int(rep)), "idempotent")
		assert.LessOrEqual(t, int(rep), p.end, "identity is in stab so rep ≤ cell")
		for u := range p.st.AllVisited() {
			related := false
			for m := stab; m != 0; m &= m - 1 {
				if s.TransformCell(mBitIndex(m), u) == p.end {
					related = true
					break
				}
			}
			assert.Equal(t, related, s.ClassRep(stab, u) == rep, "cell %d vs end %d", u, p.end)
		}
	}
}

// mBitIndex returns the transform index of the lowest set bit of m.
func mBitIndex(m uint8) uint8 {
	for t := range numTransforms {
		if m&(1<<t) != 0 {
			return uint8(t)
		}
	}
	return 0
}

// CellOrbitSize: constant on the class, equals the brute-force number of
// distinct pairs (t(K), t(cell)), stays in {1,2,4,8}, and gives 8 for a
// trivial stabilizer.
func TestCellOrbitSize(t *testing.T) {
	s := NewSymmetry(5)
	for _, p := range randomPairs(s, 1803, 120) {
		K, _, stab := s.CanonicalMaskFrame(p.st)

		seen := make(map[pairKey]bool)
		for t := range numTransforms {
			seen[pairKey{s.transformState(uint8(t), K), uint8(s.TransformCell(uint8(t), p.end))}] = true
		}
		size := s.CellOrbitSize(stab, p.end)
		assert.Equal(t, len(seen), size, "matches brute-force pair count")
		assert.Contains(t, []int{1, 2, 4, 8}, size, "power of two")

		for m := stab; m != 0; m &= m - 1 {
			assert.Equal(t, size, s.CellOrbitSize(stab, s.TransformCell(mBitIndex(m), p.end)), "constant on class")
		}
	}

	t.Run("trivial stabilizer gives 8", func(t *testing.T) {
		for _, p := range randomPairs(s, 1804, 60) {
			if p.st.CountBits() > 1 && bruteStab(s, mustFrameK(t, s, p.st)) == 1<<0 {
				assert.Equal(t, 8, s.CellOrbitSize(1<<0, p.end))
			}
		}
	})
}

// mustFrameK returns the canonical mask of st, failing the test on a mismatch
// with brute force (input sanity for the trivial-stab probe).
func mustFrameK(t *testing.T, s *Symmetry, st state.State) state.State {
	t.Helper()
	K, _, _ := s.CanonicalMaskFrame(st)
	want, _, _ := bruteClass(s, st, lastCell(st))
	require.Equal(t, want, K)
	return K
}

// lastCell returns any cell of a non-empty mask.
func lastCell(st state.State) int {
	end := 0
	for u := range st.AllVisited() {
		end = u
	}
	return end
}

// CanonicalClass == brute-force lexicographic pair minimum + distinct-pair
// count on random masks of every board size (fixed seed); it coincides with
// the reader composition through CanonicalMaskFrame and is invariant along
// the orbit.
func TestCanonicalClassMatchesBruteForce(t *testing.T) {
	for _, size := range []int{5, 6, 7, 8} {
		t.Run(fmt.Sprintf("%dx%d", size, size), func(t *testing.T) {
			s := NewSymmetry(size)
			for _, p := range randomPairs(s, int64(1805+size), 80) {
				K, rep, orbitSize := s.CanonicalClass(p.st, p.end)

				wantK, wantRep, wantOrbit := bruteClass(s, p.st, p.end)
				assert.Equal(t, wantK, K, "K")
				assert.Equal(t, wantRep, rep, "rep is the lexicographic pair minimum")
				assert.Equal(t, wantOrbit, orbitSize, "orbit size is the distinct-pair count")

				frameK, frame, stab := s.CanonicalMaskFrame(p.st)
				c := s.TransformCell(frame, p.end)
				assert.Equal(t, K, frameK, "composition mask")
				assert.Equal(t, rep, s.ClassRep(stab, c), "composition rep")
				assert.Equal(t, orbitSize, s.CellOrbitSize(stab, c), "composition orbit size")

				for g := range numTransforms {
					gK, gRep, gSize := s.CanonicalClass(s.transformState(uint8(g), p.st), s.TransformCell(uint8(g), p.end))
					assert.Equal(t, K, gK, "orbit invariant K (g=%d)", g)
					assert.Equal(t, rep, gRep, "orbit invariant rep (g=%d)", g)
					assert.Equal(t, orbitSize, gSize, "orbit invariant size (g=%d)", g)
				}
			}
		})
	}
}

// Edge case of specs/symmetry.md: on an odd board the center cell is fixed by
// all of D4, so under every mask containing it its end class has size 1 —
// ClassRep maps the center to itself and no other cell shares the tag — and
// the pair orbit degenerates to the mask orbit (8/|Stab(K)|). Deterministic
// table, no seed.
func TestCenterCellClassSingleton(t *testing.T) {
	s := NewSymmetry(5)
	const center = 12

	tests := []struct {
		name  string
		cells []int
	}{
		{name: "center alone", cells: []int{center}},
		{name: "center and corner", cells: []int{center, 0}},
		{name: "center on the diagonal", cells: []int{center, 6}},
		{name: "asymmetric appendage", cells: []int{center, 1, 5}},
		{name: "corner X keeps center class 1", cells: []int{center, 0, 4, 18, 22}},
		{name: "edge cross", cells: []int{center, 2, 10, 14, 22}},
		{name: "diamond ring", cells: []int{center, 5, 7, 17, 19}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := state.NewState(tt.cells...)
			K, rep, orbitSize := s.CanonicalClass(st, center)
			frameK, frame, stab := s.CanonicalMaskFrame(st)

			assert.Equal(t, K, frameK)
			assert.True(t, K.IsVisited(center), "center survives canonicalization")
			assert.Equal(t, uint8(center), rep, "center is fixed by every transform")
			assert.Equal(t, uint8(center), s.ClassRep(stab, s.TransformCell(frame, center)))

			// Class size 1: the tag belongs to the center alone — over ALL
			// board cells, orbits being disjoint.
			for u := range s.size * s.size {
				assert.Equalf(t, u == center, s.ClassRep(stab, u) == rep, "cell %d shares the center tag", u)
			}

			// Pair orbit = mask orbit: a fixed end adds no distinct pairs.
			distinct := make(map[state.State]bool)
			for tf := range numTransforms {
				distinct[s.transformState(uint8(tf), st)] = true
			}
			assert.Equal(t, len(distinct), orbitSize, "orbitSize = 8/|Stab(K)| for a fixed end")

			wantK, wantRep, wantOrbit := bruteClass(s, st, center)
			assert.Equal(t, wantK, K)
			assert.Equal(t, wantRep, rep)
			assert.Equal(t, wantOrbit, orbitSize)
		})
	}
}
