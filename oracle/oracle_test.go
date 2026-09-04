package oracle

import (
	"fmt"
	"math/bits"
	"math/rand"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/graph"
	"knighttour/state"
	"knighttour/symmetry"
)

// bruteForceH counts knight paths covering exactly mask and ending at end by
// plain reverse DFS without memoization — the reference implementation.
func bruteForceH(g *graph.Graph, mask state.State, end int) uint64 {
	if !mask.IsVisited(end) {
		return 0
	}
	var walk func(cur int, todo state.State) uint64
	walk = func(cur int, todo state.State) uint64 {
		if todo == 0 {
			return 1
		}
		var n uint64
		for c := range g.GetNeighborMask(cur).Intersect(todo).AllVisited() {
			n += walk(c, todo.Unvisit(c))
		}
		return n
	}
	return walk(end, mask.Unvisit(end))
}

func TestOracleExhaustiveSmall(t *testing.T) {
	const size = 5
	g := graph.New(size)
	o := New(g)

	cells := state.State(1 << (size * size))
	for m := state.State(1); m < cells; m++ {
		if bits.OnesCount64(uint64(m)) > 5 {
			continue
		}
		for e := range m.AllVisited() {
			want := bruteForceH(g, m, e)
			assert.Equalf(t, want, o.Get(m, e), "mask=%b end=%d", uint64(m), e)
		}
	}

	_, computes, classes := o.Stats()
	assert.Positive(t, computes)
	assert.LessOrEqual(t, classes, computes, "memoization must merge misses")
}

func TestOracleRandomConnected(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test seed

	for _, size := range []int{5, 6} {
		g := graph.New(size)
		o := New(g)

		for trial := range 300 {
			mask, ok := randomWalkMask(rng, g, 6+rng.Intn(9)) // sizes 6..14
			if !ok {
				continue
			}
			for e := range mask.AllVisited() {
				want := bruteForceH(g, mask, e)
				require.Equalf(t, want, o.Get(mask, e), "size=%d trial=%d mask=%b end=%d", size, trial, uint64(mask), e)
			}
		}
	}
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

func transformPair(size, orient int, mask state.State, end int) (state.State, int) {
	f := symmetry.GetSymmetries(size)[orient]
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

// Translation invariance: every translated/rotated placement of a shape yields
// the same canonical key and the same h value.
func TestOracleNormalizationInvariance(t *testing.T) {
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // deterministic test seed
	const size = 6
	g := graph.New(size)
	o := New(g)

	for trial := range 100 {
		mask, ok := randomWalkMask(rng, g, 4+rng.Intn(5))
		if !ok {
			continue
		}
		end := bits.TrailingZeros64(uint64(mask))

		var sc ShapeCtx
		o.Prepare(mask, &sc)
		wantKey := o.keyOf(&sc, end)
		wantH := bruteForceH(g, mask, end)

		for orient := range symmetry.NumTransforms {
			tmask, tend := transformPair(size, orient, mask, end)
			var scT ShapeCtx
			o.Prepare(tmask, &scT)
			require.Equalf(t, wantKey, o.keyOf(&scT, tend), "trial=%d orientation=%d", trial, orient)

			for dx := -(size - 1); dx < size; dx++ {
				for dy := -(size - 1); dy < size; dy++ {
					tr, te, ok := translatePair(size, tmask, tend, dx, dy)
					if !ok {
						continue
					}
					var scTr ShapeCtx
					o.Prepare(tr, &scTr)
					assert.Equalf(t, wantKey, o.keyOf(&scTr, te), "trial=%d orient=%d translate=(%d,%d)", trial, orient, dx, dy)
					assert.Equal(t, wantH, o.Get(tr, te))
				}
			}
		}
	}
}

type pair struct {
	mask state.State
	end  int
}

// On all pairs of shape size ≤ 4 the distinct canonical keys coincide with
// the explicit D4 ⋉ translation orbits (no collisions, no splits).
func TestOracleClassesMatchOrbits(t *testing.T) {
	const size = 5
	g := graph.New(size)
	o := New(g)

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
		for orient := range symmetry.NumTransforms {
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

	keyOfPair := func(p pair) key {
		var sc ShapeCtx
		o.Prepare(p.mask, &sc)
		return o.keyOf(&sc, p.end)
	}

	orbits := make(map[int]bool)
	keys := make(map[key]bool)
	keyOrbit := make(map[key]int)
	for i, p := range all {
		root := find(i)
		orbits[root] = true
		k := keyOfPair(p)
		keys[k] = true
		if prev, ok := keyOrbit[k]; ok {
			require.Equalf(t, prev, root, "key %v shared by orbits %d and %d", k, prev, root)
		}
		keyOrbit[k] = root
	}

	assert.Len(t, keys, len(orbits), "one canonical key per orbit")
}

func TestOracleZeroIsMemoized(t *testing.T) {
	g := graph.New(8)
	o := New(g)

	// Two cells far apart: no knight path covers both.
	mask := state.NewState(0, 7)
	require.Zero(t, o.Get(mask, 7))

	_, computes1, classes1 := o.Stats()
	require.Zero(t, o.Get(mask, 7)) // served from table
	_, computes2, classes2 := o.Stats()
	assert.Equal(t, computes1, computes2, "zero values must be stored, not recomputed")
	assert.Equal(t, classes1, classes2)

	// end outside the mask is zero as well.
	assert.Zero(t, o.Get(mask, 3))
}

func TestOraclePreparedMatchesGet(t *testing.T) {
	rng := rand.New(rand.NewSource(5)) //nolint:gosec // deterministic test seed
	g := graph.New(7)
	o := New(g)

	mask, ok := randomWalkMask(rng, g, 12)
	require.True(t, ok)
	var sc ShapeCtx
	o.Prepare(mask, &sc)
	for e := range mask.AllVisited() {
		assert.Equal(t, o.Get(mask, e), o.GetPrepared(&sc, e))
	}
}

// perms[t] must be a bijection on [0,total) matching symmetry.GetSymmetries,
// and rows/cols must agree with pos decomposition. This guards the orientation
// ordering that Prepare's orR/orC tables depend on.
func TestOraclePermsMatchSymmetries(t *testing.T) {
	for _, size := range []int{5, 6, 7, 8} {
		t.Run(fmt.Sprintf("size%d", size), func(t *testing.T) {
			g := graph.New(size)
			o := New(g)
			total := g.GetTotalCells()
			closures := symmetry.GetSymmetries(size)

			for tIdx, f := range closures {
				seen := make([]bool, total)
				for pos := range total {
					got := int(o.perms[tIdx][pos])
					require.GreaterOrEqual(t, got, 0)
					require.Less(t, got, total, "perm out of range")
					require.Falsef(t, seen[got], "orient %d not injective at pos %d", tIdx, pos)
					seen[got] = true

					nx, ny := f(pos/size, pos%size, size)
					assert.Equalf(t, nx*size+ny, got, "orient %d pos %d mismatch", tIdx, pos)
				}
			}
			for pos := range total {
				assert.Equalf(t, pos, int(o.rows[pos])*size+int(o.cols[pos]), "rows/cols pos %d", pos)
			}
		})
	}
}

func TestOracleShardIndexRange(t *testing.T) {
	rng := rand.New(rand.NewSource(11)) //nolint:gosec // deterministic test seed
	hit := make(map[int]bool)
	for range 20000 {
		k := key{shape: state.State(rng.Uint64()), end: uint8(rng.Intn(maxCells))}
		si := shardIndex(k)
		require.GreaterOrEqual(t, si, 0)
		assert.Lessf(t, si, numShards, "shard index out of range")
		hit[si] = true
	}
	assert.Lenf(t, hit, numShards, "all shards must be reachable")
}

func TestOraclePrepareSingleCell(t *testing.T) {
	for _, size := range []int{5, 8} {
		g := graph.New(size)
		o := New(g)

		var wantKey key
		for p := range g.GetTotalCells() {
			mask := state.Bit(p)
			var sc ShapeCtx
			o.Prepare(mask, &sc)
			k := o.keyOf(&sc, p)
			assert.Equalf(t, 1, k.shape.CountBits(), "pos %d shape bits", p)
			assert.Zero(t, k.end, "single cell normalizes end to origin")

			if p == 0 {
				wantKey = k
			}
			assert.Equalf(t, wantKey, k, "key must be position independent (pos %d)", p)

			for orient := range symmetry.NumTransforms {
				tmask, tend := transformPair(size, orient, mask, p)
				var scT ShapeCtx
				o.Prepare(tmask, &scT)
				assert.Equalf(t, wantKey, o.keyOf(&scT, tend), "pos %d orient %d", p, orient)
			}

			assert.Equalf(t, uint64(1), o.Get(mask, p), "h of single cell pos %d", p)
		}
	}
}

func TestOraclePrepareFullBoard(t *testing.T) {
	const size = 8
	g := graph.New(size)
	o := New(g)
	total := g.GetTotalCells() // == maxCells, exercises the pos[..] boundary.

	var full state.State
	for p := range total {
		full = full.Visit(p)
	}
	end := total - 1

	var sc ShapeCtx
	require.NotPanics(t, func() { o.Prepare(full, &sc) })

	wantKey := o.keyOf(&sc, end)
	assert.Equal(t, total, wantKey.shape.CountBits())
	assert.Less(t, int(wantKey.end), total)

	for tIdx := range symmetry.NumTransforms {
		assert.Equalf(t, total, sc.shapes[tIdx].CountBits(), "orient %d must keep all bits", tIdx)
		tmask, tend := transformPair(size, tIdx, full, end)
		var scT ShapeCtx
		o.Prepare(tmask, &scT)
		assert.Equalf(t, wantKey, o.keyOf(&scT, tend), "orient %d key invariant", tIdx)
	}
}

// Masks whose bbox spans the entire board exercise every orR/orC offset; all
// eight normalized shapes must preserve the bit count and stay within [0,total),
// which catches any byte underflow in Prepare's translation math.
func TestOraclePrepareFullSpanBBox(t *testing.T) {
	for _, size := range []int{5, 6, 7, 8} {
		t.Run(fmt.Sprintf("size%d", size), func(t *testing.T) {
			g := graph.New(size)
			o := New(g)
			total := g.GetTotalCells()
			last := size - 1

			masks := []state.State{
				state.NewState(0, last, last*size, last*size+last),                       // corners
				state.NewState(0, last, last*size, last*size+last, (last/2)*size+last/2), // corners + center
				state.NewState(last/2, last*size+last/2+1, (last/2)*size, (last/2-1)*size+last),
			}

			for mi, mask := range masks {
				require.Equalf(t, size-1, maxRow(o, mask)-minRow(o, mask), "mask %d must span rows", mi)
				require.Equalf(t, size-1, maxCol(o, mask)-minCol(o, mask), "mask %d must span cols", mi)

				var sc ShapeCtx
				o.Prepare(mask, &sc)
				end := bits.TrailingZeros64(uint64(mask))

				for tIdx := range symmetry.NumTransforms {
					assert.Equalf(t, mask.CountBits(), sc.shapes[tIdx].CountBits(),
						"mask %d orient %d bit drop", mi, tIdx)
					for p := range sc.shapes[tIdx].AllVisited() {
						assert.Lessf(t, p, total, "mask %d orient %d coord out of board", mi, tIdx)
					}
				}

				wantKey := o.keyOf(&sc, end)
				for tIdx := range symmetry.NumTransforms {
					tmask, tend := transformPair(size, tIdx, mask, end)
					var scT ShapeCtx
					o.Prepare(tmask, &scT)
					assert.Equalf(t, wantKey, o.keyOf(&scT, tend), "mask %d orient %d key", mi, tIdx)
				}
			}
		})
	}
}

func TestOracleShapeCtxReuse(t *testing.T) {
	rng := rand.New(rand.NewSource(23)) //nolint:gosec // deterministic test seed
	g := graph.New(7)
	o := New(g)

	a, okA := randomWalkMask(rng, g, 10)
	require.True(t, okA)
	b, okB := randomWalkMask(rng, g, 6)
	require.True(t, okB)

	var sc ShapeCtx
	check := func(mask state.State, tag string) {
		o.Prepare(mask, &sc)
		for e := range mask.AllVisited() {
			assert.Equalf(t, bruteForceH(g, mask, e), o.GetPrepared(&sc, e), "%s end %d", tag, e)
		}
	}

	check(a, "a-first")
	check(b, "b-after-a")
	check(a, "a-reused") // stale dxs/dys from b must not leak.

	// Reuse after a full-board prepare (largest bbox) then shrink back.
	var full state.State
	for p := range g.GetTotalCells() {
		full = full.Visit(p)
	}
	o.Prepare(full, &sc)
	check(b, "b-after-full")
}

func minRow(o *Oracle, mask state.State) int {
	m := 1 << 30
	for p := range mask.AllVisited() {
		m = min(m, int(o.rows[p]))
	}
	return m
}
func maxRow(o *Oracle, mask state.State) int {
	m := -1
	for p := range mask.AllVisited() {
		m = max(m, int(o.rows[p]))
	}
	return m
}
func minCol(o *Oracle, mask state.State) int {
	m := 1 << 30
	for p := range mask.AllVisited() {
		m = min(m, int(o.cols[p]))
	}
	return m
}
func maxCol(o *Oracle, mask state.State) int {
	m := -1
	for p := range mask.AllVisited() {
		m = max(m, int(o.cols[p]))
	}
	return m
}

func TestOracleConcurrent(t *testing.T) {
	g := graph.New(6)
	o := New(g)

	mask, ok := randomWalkMask(rand.New(rand.NewSource(9)), g, 10) //nolint:gosec // deterministic test seed
	require.True(t, ok)
	ends := make([]int, 0, mask.CountBits())
	for e := range mask.AllVisited() {
		ends = append(ends, e)
	}

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for range 50 {
				for _, e := range ends {
					if got, want := o.Get(mask, e), bruteForceH(g, mask, e); got != want {
						t.Errorf("worker %d: Get(%b,%d)=%d want %d", w, uint64(mask), e, got, want)
					}
				}
			}
		}(w)
	}
	wg.Wait()
}
