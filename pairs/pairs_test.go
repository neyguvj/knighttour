package pairs

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"knighttour/cache"
	"knighttour/graph"
	"knighttour/searcher"
	"knighttour/symmetry"
)

// Directed board totals from docs/requirements.md (open tours counted per
// direction): the Σ_{s,t} anchor of the matrix.
const (
	total5x5 uint64 = 1_728
	total6x6 uint64 = 6_637_920
)

// naiveRows is the independent brute-force oracle: plain backtracking over
// adjacency lists with a visited slice — no bitboards, no pruning, no
// symmetry. rows[s][t] counts directed Hamiltonian paths from s to t, one
// increment per full-length leaf.
func naiveRows(g *graph.Graph) [][]uint64 {
	total := g.GetTotalCells()
	rows := make([][]uint64, total)
	for s := range total {
		rows[s] = make([]uint64, total)
	}

	visited := make([]bool, total)
	var walk func(start, cur, covered int)
	walk = func(start, cur, covered int) {
		if covered == total {
			rows[start][cur]++
			return
		}
		for _, n := range g.GetNeighbors(cur) {
			if !visited[n] {
				visited[n] = true
				walk(start, n, covered+1)
				visited[n] = false
			}
		}
	}

	for s := range total {
		visited[s] = true
		walk(s, s, 1)
		visited[s] = false
	}
	return rows
}

// toursFromStart is the independent row-sum reference: GenerateTasks at depth
// totalCells writes every full completion with weight 1, so the summed table
// weights equal the tour count from start (specs/pairs.md).
func toursFromStart(tb testing.TB, g *graph.Graph, start int) uint64 {
	tb.Helper()
	c := cache.NewCache()
	searcher.NewSearcher(g, symmetry.NewSymmetry(g.Size())).
		GenerateTasks(context.Background(), c, start, 1, g.GetTotalCells())
	var sum uint64
	for e := range c.Seal().All(context.Background()) {
		sum += e.Weight
	}
	return sum
}

// The oracle test: Compute and Row match the naive pair enumeration on every
// cell of 5×5, pinning the anchored descent, the D4 completion and the row-sum
// anchor against searcher.GenerateTasks at full depth.
func TestMatrixAndRowsMatchOracleOn5x5(t *testing.T) {
	g := graph.New(5)
	want := naiveRows(g)
	got := Compute(context.Background(), g, 3)

	cells := g.GetTotalCells()
	for s := range cells {
		row := Row(context.Background(), g, s)
		for tt := range cells {
			assert.Equalf(t, want[s][tt], got.At(s, tt), "At(%d,%d)", s, tt)
			assert.Equalf(t, want[s][tt], row[tt], "Row(%d)[%d]", s, tt)
		}
		assert.Equalf(t, toursFromStart(t, g, s), sum(row), "Σ_t Row(%d)[t] vs GenerateTasks", s)
	}
	assert.Equal(t, total5x5, got.Total(), "Σ_{s,t} on 5×5")
}

// Inversion symmetry and D4 equivariance of the filled matrix, plus the
// diagonal edge case N(s,s) = 0. The 6×6 board is covered inside
// TestMatrixOn6x6 over one shared matrix (the plan-28 budget rule).
func TestMatrixSymmetryAndD4Equivariance(t *testing.T) {
	g := graph.New(5)
	assertSymmetryAndD4(t, g, Compute(context.Background(), g, 2))
}

// Zero cells match the color classification: on an even board only opposite
// colors pair up, on an odd board both ends must carry the majority color.
func TestColorParityZeros(t *testing.T) {
	g := graph.New(5)
	assertParityZeros(t, g, Compute(context.Background(), g, 2))
}

// assertSymmetryAndD4 checks At(s,t) = At(t,s), the equivariance
// N(σs,σt) = N(s,t) over all eight transforms and pairs, and N(s,s) = 0.
func assertSymmetryAndD4(tb testing.TB, g *graph.Graph, m *Matrix) {
	tb.Helper()
	sym := symmetry.NewSymmetry(g.Size())
	cells := g.GetTotalCells()

	for s := range cells {
		assert.Zero(tb, m.At(s, s), "N(s,s) must be zero")
		for tt := range cells {
			assert.Equalf(tb, m.At(s, tt), m.At(tt, s), "At(%d,%d) vs At(%d,%d)", s, tt, tt, s)
		}
	}
	for tr := range symmetry.NumTransforms {
		for s := range cells {
			for tt := range cells {
				ms := sym.TransformCell(uint8(tr), s)
				mt := sym.TransformCell(uint8(tr), tt)
				assert.Equalf(tb, m.At(s, tt), m.At(ms, mt), "N(σ%d s, σ%d t)", tr, tr)
			}
		}
	}
}

// assertParityZeros checks that every parity-forbidden cell of the matrix is
// zero (see parityForbids).
func assertParityZeros(tb testing.TB, g *graph.Graph, m *Matrix) {
	tb.Helper()
	majority := majorityColor(g)
	cells := g.GetTotalCells()
	for s := range cells {
		for tt := range cells {
			if parityForbids(g, majority, s, tt) {
				assert.Zero(tb, m.At(s, tt), "parity must zero At(%d,%d)", s, tt)
			}
		}
	}
}

// cellColor is the chessboard color (row+col) parity of a cell — the parity
// every knight move flips.
func cellColor(g *graph.Graph, cell int) int {
	return (cell/g.Size() + cell%g.Size()) % 2
}

// majorityColor returns the color holding ⌈cells/2⌉ cells: the only color an
// open tour can start and end on on an odd board.
func majorityColor(g *graph.Graph) int {
	even := 0
	for cell := range g.GetTotalCells() {
		if cellColor(g, cell) == 0 {
			even++
		}
	}
	if even*2 > g.GetTotalCells() {
		return 0
	}
	return 1
}

// parityForbids reports whether a directed Hamiltonian path from s to t is
// impossible by color parity: the path alternates colors, so on an even board
// the ends differ and on an odd board both sit on the majority color.
func parityForbids(g *graph.Graph, majority, s, t int) bool {
	if g.Size()%2 == 0 {
		return cellColor(g, s) == cellColor(g, t)
	}
	return cellColor(g, s) != majority || cellColor(g, t) != majority
}

// Worker invariance and run-to-run determinism: the filled matrix is bit-exact
// for any crew size. The crew logic is board-independent; 6×6 reuses the same
// Run barrier as the shared matrix of TestMatrixOn6x6 (the plan-28 budget).
func TestComputeDeterministicAcrossWorkers(t *testing.T) {
	g := graph.New(5)
	first := Compute(context.Background(), g, 1)
	assert.Equal(t, first, Compute(context.Background(), g, 1), "run-to-run determinism")
	for _, workers := range []int{2, 4} {
		assert.Equal(t, first, Compute(context.Background(), g, workers), "workers invariance")
	}
}

// A terminated ctx stops the descent and the pool claiming: Row and Compute
// return a partial result without panicking. Pre-terminated contexts must
// yield zeroes; a short live deadline only bounds the work (either outcome is
// correct, so just the bound against the full matrix is asserted).
func TestTerminatedContextPartialResult(t *testing.T) {
	g := graph.New(5)
	fullRow := Row(context.Background(), g, 0)
	fullMatrix := Compute(context.Background(), g, 2)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	elapsed, stopElapsed := context.WithTimeout(context.Background(), 0)
	defer stopElapsed()
	tiny, stopTiny := context.WithTimeout(context.Background(), time.Microsecond)
	defer stopTiny()

	tests := []struct {
		ctx     context.Context
		name    string
		wantNil bool
	}{
		{name: "canceled", ctx: canceled, wantNil: true},
		{name: "deadline elapsed", ctx: elapsed, wantNil: true},
		{name: "deadline during run", ctx: tiny, wantNil: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := Row(tt.ctx, g, 0)
			m := Compute(tt.ctx, g, 2)

			assert.Len(t, row, g.GetTotalCells())
			if tt.wantNil {
				assert.Zero(t, sum(row))
				assert.Zero(t, m.Total())
			}
			for c, w := range row {
				assert.LessOrEqual(t, w, fullRow[c], "partial row cell %d", c)
			}
			assert.LessOrEqual(t, m.Total(), fullMatrix.Total())
		})
	}
}

// The MITM probe reproduces Row on every 5×5 row at the balanced cut. On 6×6
// the census of plan-28 step 3 (m = 18: 402 530 prefix walks glued against
// 5 453 628 992 suffix walks, 752 s and 5.3 GB peak without -race) put the
// probe over the 60 s unit gate, so it lives there only as the gated point of
// BenchmarkPairsProbe.
func TestMitmMatchesRowOn5x5(t *testing.T) {
	g := graph.New(5)
	m := (g.GetTotalCells() + 1) / 2
	for start := range g.GetTotalCells() {
		assert.Equalf(t, Row(context.Background(), g, start), mitmRow(context.Background(), g, start, m),
			"mitmRow vs Row on start %d", start)
	}
}

// The heavy 6×6 checks share one computed matrix (the plan-28 budget rule:
// orbit sampling in units): the Σ_{s,t} anchor, symmetry/equivariance and
// parity on the filled table, restored rows against direct Row on a sample of
// orbit members spanning every orbit size, and row sums against full-depth
// GenerateTasks on two sampled starts.
func TestMatrixOn6x6(t *testing.T) {
	g := graph.New(6)
	m := Compute(context.Background(), g, 4)

	assert.Equal(t, total6x6, m.Total(), "Σ_{s,t} on 6×6")
	assertSymmetryAndD4(t, g, m)
	assertParityZeros(t, g, m)

	t.Run("RestoredRowsMatchDirectRow", func(t *testing.T) {
		for _, start := range sampleOrbitMembers(g) {
			row := Row(context.Background(), g, start)
			for tt := range g.GetTotalCells() {
				assert.Equalf(t, row[tt], m.At(start, tt), "restored At(%d,%d) vs Row", start, tt)
			}
		}
	})

	t.Run("RowSumsMatchGenerateTasks", func(t *testing.T) {
		for _, start := range sampleStarts(g) {
			assert.Equalf(t, toursFromStart(t, g, start), sum(Row(context.Background(), g, start)),
				"Σ_t Row(%d)[t] vs GenerateTasks", start)
		}
	})
}

// sampleOrbitMembers returns non-canonical cells spanning every orbit size of
// the board (up to two per size): the direct-Row cross-check of the D4
// completion on the sampled orbit members of specs/pairs.md.
func sampleOrbitMembers(g *graph.Graph) []int {
	perSize := make(map[int][]int)
	for _, group := range symmetry.NewSymmetry(g.Size()).GetCanonicalGroups() {
		if len(group.Positions) > 1 && len(perSize[group.OrbitSize]) < 2 {
			perSize[group.OrbitSize] = append(perSize[group.OrbitSize], group.Positions[1])
		}
	}
	var out []int
	for _, cells := range perSize {
		out = append(out, cells...)
	}
	slices.Sort(out) // deterministic sample order regardless of map iteration
	return out
}

// sampleStarts picks the deterministic starts of the GenerateTasks anchor on
// 6×6: the first canonical representative and one member of the last orbit.
func sampleStarts(g *graph.Graph) []int {
	groups := symmetry.NewSymmetry(g.Size()).GetCanonicalGroups()
	last := groups[len(groups)-1]
	if len(last.Positions) > 1 {
		return []int{groups[0].Canonical, last.Positions[len(last.Positions)-1]}
	}
	return []int{groups[0].Canonical, groups[0].Canonical}
}

// sum adds the cells of a row vector.
func sum(row []uint64) uint64 {
	var total uint64
	for _, w := range row {
		total += w
	}
	return total
}
