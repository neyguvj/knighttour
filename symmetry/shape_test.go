package symmetry

import (
	"fmt"
	"math/bits"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/graph"
	"knighttour/path"
	"knighttour/state"
)

func transformPair(size, orient int, mask state.State, end int) (state.State, int) {
	f := GetSymmetries(size)[orient]
	var out state.State
	for p := range mask.AllVisited() {
		nx, ny := f(p/size, p%size, size)
		out = out.Visit(nx*size + ny)
	}
	ex, ey := f(end/size, end%size, size)
	return out, ex*size + ey
}

func translatePair(size int, mask state.State, end, dx, dy int) (state.State, int, bool) {
	var out state.State
	for p := range mask.AllVisited() {
		nx, ny := p/size+dx, p%size+dy
		if nx < 0 || nx >= size || ny < 0 || ny >= size {
			return 0, 0, false
		}
		out = out.Visit(nx*size + ny)
	}
	ex, ey := end/size+dx, end%size+dy
	if ex < 0 || ex >= size || ey < 0 || ey >= size {
		return 0, 0, false
	}
	return out, ex*size + ey, true
}

func randomWalkMask(rng *rand.Rand, g *graph.Graph, length int) (state.State, bool) {
	for range 100 {
		mask := state.Bit(rng.Intn(g.GetTotalCells()))
		cur := bits.TrailingZeros64(uint64(mask))
		grew := true
		for range length - 1 {
			var cand []int
			for n := range g.GetNeighborMask(cur).AllVisited() {
				if mask.IsUnvisited(n) {
					cand = append(cand, n)
				}
			}
			if len(cand) == 0 {
				grew = false
				break
			}
			cur = cand[rng.Intn(len(cand))]
			mask = mask.Visit(cur)
		}
		if grew && mask.CountBits() == length {
			return mask, true
		}
	}
	return 0, false
}

// Every translated/rotated placement of a shape yields the same canonical key.
func TestShapeNormalizationInvariance(t *testing.T) {
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // deterministic test seed
	const size = 6
	g := graph.New(size)
	sym := NewSymmetry(size)

	for trial := range 100 {
		mask, ok := randomWalkMask(rng, g, 4+rng.Intn(5))
		if !ok {
			continue
		}
		end := bits.TrailingZeros64(uint64(mask))

		var sc ShapeCtx
		sym.PrepareShape(mask, &sc)
		wantKey := sym.KeyFromPrepared(&sc, end)

		for orient := range NumTransforms {
			tmask, tend := transformPair(size, orient, mask, end)
			assert.Equalf(t, wantKey, sym.CanonicalizeShape(tmask, tend), "trial=%d orientation=%d", trial, orient)

			for dx := -(size - 1); dx < size; dx++ {
				for dy := -(size - 1); dy < size; dy++ {
					tr, te, ok := translatePair(size, tmask, tend, dx, dy)
					if !ok {
						continue
					}
					assert.Equalf(t, wantKey, sym.CanonicalizeShape(tr, te),
						"trial=%d orient=%d translate=(%d,%d)", trial, orient, dx, dy)
				}
			}
		}
	}
}

type pair struct {
	mask state.State
	end  int
}

// On all pairs of shape size ≤ 4 the distinct canonical keys coincide with the
// explicit D4 ⋉ translation orbits (no collisions, no splits).
func TestShapeClassesMatchOrbits(t *testing.T) {
	const size = 5
	sym := NewSymmetry(size)

	var all []pair
	idx := make(map[pair]int)
	for m := state.State(1); m < state.Bit(size*size); m++ {
		if bits.OnesCount64(uint64(m)) > 4 {
			continue
		}
		for e := range m.AllVisited() {
			idx[pair{m, e}] = len(all)
			all = append(all, pair{m, e})
		}
	}

	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}
	var find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	for i, p := range all {
		for orient := range NumTransforms {
			nm, ne := transformPair(size, orient, p.mask, p.end)
			for dx := -(size - 1); dx < size; dx++ {
				for dy := -(size - 1); dy < size; dy++ {
					tr, te, ok := translatePair(size, nm, ne, dx, dy)
					if !ok {
						continue
					}
					if j, ok := idx[pair{tr, te}]; ok {
						union(i, j)
					}
				}
			}
		}
	}

	orbits := make(map[int]bool)
	keys := make(map[path.Path]bool)
	keyOrbit := make(map[path.Path]int)
	for i, p := range all {
		root := find(i)
		orbits[root] = true
		k := sym.CanonicalizeShape(p.mask, p.end)
		keys[k] = true
		if prev, ok := keyOrbit[k]; ok {
			require.Equalf(t, prev, root, "key %v shared by orbits %d and %d", k, prev, root)
		}
		keyOrbit[k] = root
	}

	assert.Len(t, keys, len(orbits), "one canonical key per orbit")
}

func TestShapePrepareSingleCell(t *testing.T) {
	for _, size := range []int{5, 8} {
		g := graph.New(size)
		sym := NewSymmetry(size)

		var wantKey path.Path
		for p := range g.GetTotalCells() {
			mask := state.Bit(p)
			k := sym.CanonicalizeShape(mask, p)
			assert.Equalf(t, 1, k.State().CountBits(), "pos %d shape bits", p)
			assert.Zero(t, k.End(), "single cell normalizes end to origin")

			if p == 0 {
				wantKey = k
			}
			assert.Equalf(t, wantKey, k, "key must be position independent (pos %d)", p)

			for orient := range NumTransforms {
				tmask, tend := transformPair(size, orient, mask, p)
				assert.Equalf(t, wantKey, sym.CanonicalizeShape(tmask, tend), "pos %d orient %d", p, orient)
			}
		}
	}
}

func TestShapePrepareFullBoard(t *testing.T) {
	const size = 8
	g := graph.New(size)
	sym := NewSymmetry(size)
	total := g.GetTotalCells() // == maxCells, exercises the pos[..] boundary.

	var full state.State
	for p := range total {
		full = full.Visit(p)
	}
	end := total - 1

	var sc ShapeCtx
	require.NotPanics(t, func() { sym.PrepareShape(full, &sc) })

	wantKey := sym.KeyFromPrepared(&sc, end)
	assert.Equal(t, total, wantKey.State().CountBits())
	assert.Less(t, wantKey.End(), total)

	for tIdx := range NumTransforms {
		assert.Equalf(t, total, sc.shapes[tIdx].CountBits(), "orient %d must keep all bits", tIdx)
		tmask, tend := transformPair(size, tIdx, full, end)
		var scT ShapeCtx
		sym.PrepareShape(tmask, &scT)
		assert.Equalf(t, wantKey, sym.KeyFromPrepared(&scT, tend), "orient %d key invariant", tIdx)
	}
}

func minMaxColRow(mask state.State, size int) (minR, maxR, minC, maxC int) {
	minR, minC = 1<<30, 1<<30
	maxR, maxC = -1, -1
	for p := range mask.AllVisited() {
		minR, maxR = min(minR, p/size), max(maxR, p/size)
		minC, maxC = min(minC, p%size), max(maxC, p%size)
	}
	return minR, maxR, minC, maxC
}

// Masks whose bbox spans the entire board exercise every orR/orC offset; all
// eight normalized shapes must preserve the bit count and stay within [0,total),
// which catches any byte underflow in PrepareShape's translation math.
func TestShapePrepareFullSpanBBox(t *testing.T) {
	for _, size := range []int{5, 6, 7, 8} {
		t.Run(fmt.Sprintf("size%d", size), func(t *testing.T) {
			g := graph.New(size)
			sym := NewSymmetry(size)
			total := g.GetTotalCells()
			last := size - 1

			masks := []state.State{
				state.NewState(0, last, last*size, last*size+last),                       // corners
				state.NewState(0, last, last*size, last*size+last, (last/2)*size+last/2), // corners + center
				state.NewState(last/2, last*size+last/2+1, (last/2)*size, (last/2-1)*size+last),
			}

			for mi, mask := range masks {
				minR, maxR, minC, maxC := minMaxColRow(mask, size)
				require.Equalf(t, last, maxR-minR, "mask %d must span rows", mi)
				require.Equalf(t, last, maxC-minC, "mask %d must span cols", mi)

				var sc ShapeCtx
				sym.PrepareShape(mask, &sc)
				end := bits.TrailingZeros64(uint64(mask))

				for tIdx := range NumTransforms {
					assert.Equalf(t, mask.CountBits(), sc.shapes[tIdx].CountBits(),
						"mask %d orient %d bit drop", mi, tIdx)
					for p := range sc.shapes[tIdx].AllVisited() {
						assert.Lessf(t, p, total, "mask %d orient %d coord out of board", mi, tIdx)
					}
				}

				wantKey := sym.KeyFromPrepared(&sc, end)
				for tIdx := range NumTransforms {
					tmask, tend := transformPair(size, tIdx, mask, end)
					var scT ShapeCtx
					sym.PrepareShape(tmask, &scT)
					assert.Equalf(t, wantKey, sym.KeyFromPrepared(&scT, tend), "mask %d orient %d key", mi, tIdx)
				}
			}
		})
	}
}

func TestShapeCtxReuse(t *testing.T) {
	rng := rand.New(rand.NewSource(23)) //nolint:gosec // deterministic test seed
	g := graph.New(7)
	sym := NewSymmetry(g.Size())

	a, okA := randomWalkMask(rng, g, 10)
	require.True(t, okA)
	b, okB := randomWalkMask(rng, g, 6)
	require.True(t, okB)

	keysOf := func(mask state.State) map[int]path.Path {
		out := make(map[int]path.Path)
		for e := range mask.AllVisited() {
			out[e] = sym.CanonicalizeShape(mask, e)
		}
		return out
	}
	check := func(mask state.State, want map[int]path.Path, tag string) {
		var sc ShapeCtx
		sym.PrepareShape(mask, &sc)
		for e, k := range want {
			assert.Equalf(t, k, sym.KeyFromPrepared(&sc, e), "%s end %d", tag, e)
		}
	}

	wantA, wantB := keysOf(a), keysOf(b)
	check(a, wantA, "a-first")
	check(b, wantB, "b-after-a")
	check(a, wantA, "a-reused") // stale dxs/dys from b must not leak.

	var full state.State
	for p := range g.GetTotalCells() {
		full = full.Visit(p)
	}
	var sc ShapeCtx
	sym.PrepareShape(full, &sc)
	check(b, wantB, "b-after-full")
}
