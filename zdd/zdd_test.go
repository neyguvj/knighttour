package zdd

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- brute-force oracle -----------------------------------------------------

// hamiltonianSets enumerates every edge subset of the graph and keeps the
// members of family fam (path or cycle criterion below). It is the independent
// oracle for the ZDD construction on small graphs (plan 23).
func hamiltonianSets(t *testing.T, n int, edges [][2]int, fam Family) [][]int {
	t.Helper()
	require.Less(t, len(edges), 20, "oracle budget: use graphs with fewer than 20 edges")
	if fam == Cycles {
		return filterEdgeSets(n, edges, isHamiltonianCycle)
	}
	if n < 2 {
		return nil // isolated-vertex convention: single vertex has no path family
	}
	return filterEdgeSets(n, edges, isHamiltonianPath)
}

// filterEdgeSets expands all subset bitmasks and keeps the sets accepted by
// pred.
func filterEdgeSets(n int, edges [][2]int, pred func(int, [][2]int, []int) bool) [][]int {
	var out [][]int
	for mask := range 1 << len(edges) {
		set := chosenEdges(mask, edges)
		if pred(n, edges, set) {
			out = append(out, set)
		}
	}
	return out
}

// isHamiltonianCycle reports whether the edge-index set forms a Hamiltonian
// cycle: size n, degree exactly 2 everywhere, and one component — equivalently
// (for n ≥ 3 simple graphs) exactly n edges whose DSU run closes one cycle.
func isHamiltonianCycle(n int, edges [][2]int, set []int) bool {
	if len(set) != n {
		return false
	}
	dsu := newDSU(n)
	deg := make([]int, n)
	closings := 0
	for _, k := range set {
		u, v := edges[k][0], edges[k][1]
		deg[u]++
		deg[v]++
		if deg[u] > 2 || deg[v] > 2 {
			return false
		}
		if !dsu.union(u, v) {
			closings++ // the edge that closes the cycle; only one may exist
		}
	}
	if closings != 1 {
		return false
	}
	root := dsu.find(0)
	for v := 1; v < n; v++ {
		if dsu.find(v) != root {
			return false
		}
	}
	return true
}

// chosenEdges expands a subset bitmask into ascending original edge indices.
func chosenEdges(mask int, edges [][2]int) []int {
	var set []int
	for k := range len(edges) {
		if mask&(1<<k) != 0 {
			set = append(set, k)
		}
	}
	return set
}

// isHamiltonianPath reports whether the edge-index set forms a spanning path:
// size n−1, degrees ≤ 2, and all vertices in one component.
func isHamiltonianPath(n int, edges [][2]int, set []int) bool {
	if len(set) != n-1 {
		return false
	}
	dsu := newDSU(n)
	deg := make([]int, n)
	for _, k := range set {
		u, v := edges[k][0], edges[k][1]
		deg[u]++
		deg[v]++
		if deg[u] > 2 || deg[v] > 2 {
			return false
		}
		if !dsu.union(u, v) {
			return false // cycle inside n−1 edges ⇒ disconnected support
		}
	}
	root := dsu.find(0)
	for v := 1; v < n; v++ {
		if dsu.find(v) != root {
			return false
		}
	}
	return true
}

// dsu is a union-find over the oracle's small vertex sets.
type dsu struct {
	parent []int
	rank   []int
}

// newDSU returns a union-find over n singleton vertices.
func newDSU(n int) *dsu {
	d := &dsu{parent: make([]int, n), rank: make([]int, n)}
	for v := range d.parent {
		d.parent[v] = v
	}
	return d
}

// find returns the representative of v's component (path halving).
func (d *dsu) find(v int) int {
	for d.parent[v] != v {
		d.parent[v] = d.parent[d.parent[v]]
		v = d.parent[v]
	}
	return v
}

// union merges two components, reporting false when they were already one.
func (d *dsu) union(u, v int) bool {
	ru, rv := d.find(u), d.find(v)
	if ru == rv {
		return false
	}
	if d.rank[ru] < d.rank[rv] {
		ru, rv = rv, ru
	}
	d.parent[rv] = ru
	if d.rank[ru] == d.rank[rv] {
		d.rank[ru]++
	}
	return true
}

// --- small graph builders ---------------------------------------------------

// completeGraph returns the edges of K_n over vertices 0..n−1.
func completeGraph(n int) [][2]int {
	var e [][2]int
	for i := range n {
		for j := i + 1; j < n; j++ {
			e = append(e, [2]int{i, j})
		}
	}
	return e
}

// pathGraph returns the edges of the simple path P_n: (i, i+1) for i < n−1.
func pathGraph(n int) [][2]int {
	e := make([][2]int, 0, max(n-1, 0))
	for i := range n - 1 {
		e = append(e, [2]int{i, i + 1})
	}
	return e
}

// cycleGraph returns the edges of the simple cycle C_n: P_n closed by (n−1, 0).
func cycleGraph(n int) [][2]int {
	e := pathGraph(n)
	return append(e, [2]int{n - 1, 0})
}

// starEdges returns the edges of K_{1,leaves}; vertex 0 is the center.
func starEdges(leaves int) [][2]int {
	var e [][2]int
	for i := 1; i <= leaves; i++ {
		e = append(e, [2]int{0, i})
	}
	return e
}

// petersenEdges returns the 15 edges of the Petersen graph on vertices
// 0..4 (outer pentagon) and 5..9 (inner pentagram + spokes).
func petersenEdges() [][2]int {
	e := make([][2]int, 0, 15)
	for i := range 5 {
		e = append(e, [2]int{i, (i + 1) % 5})     // outer pentagon
		e = append(e, [2]int{i, i + 5})           // spoke
		e = append(e, [2]int{5 + i, 5 + (i+2)%5}) // inner pentagram
	}
	return e
}

// --- table-driven equivalence ------------------------------------------------

// familyCase is one equivalence case: an input graph plus the expected number
// of family sets from a closed formula (want; nil — oracle-only case).
type familyCase struct {
	want  *uint64
	name  string
	edges [][2]int
	order []int
	fam   Family
	n     int
}

// u64 returns a pointer to v, the table notation for an explicit want.
func u64(v uint64) *uint64 { return &v }

// familyCases pin the construction against closed formulas (specs/zdd.md
// «Тесты»): Paths — K_n gives n!/2 sets, P_n one, C_n n; Cycles — K_n gives
// (n−1)!/2 sets, C_n one, P_n none. Disconnected graphs and stars give none;
// Petersen is hypohamiltonian (zero cycles, nonzero paths). Every case is
// additionally checked against the brute-force oracle, so want only restates
// the closed formula.
var familyCases = []familyCase{
	{name: "P_2", n: 2, edges: pathGraph(2), want: u64(1)},
	{name: "P_5", n: 5, edges: pathGraph(5), want: u64(1)},
	{name: "C_3=K_3", n: 3, edges: cycleGraph(3), want: u64(3)},
	{name: "C_6", n: 6, edges: cycleGraph(6), want: u64(6)},
	{name: "K_2", n: 2, edges: completeGraph(2), want: u64(1)},
	{name: "K_3", n: 3, edges: completeGraph(3), want: u64(3)},
	{name: "K_4", n: 4, edges: completeGraph(4), want: u64(12)},
	{name: "K_5", n: 5, edges: completeGraph(5), want: u64(60)},
	{name: "K_6", n: 6, edges: completeGraph(6), want: u64(360)},
	{name: "star K_1,4", n: 5, edges: starEdges(4), want: u64(0)},
	{name: "two triangles", n: 6, edges: [][2]int{{0, 1}, {1, 2}, {0, 2}, {3, 4}, {4, 5}, {3, 5}}, want: u64(0)},
	{name: "isolated vertex", n: 3, edges: [][2]int{{0, 1}}, want: u64(0)},
	// Petersen is hypohamiltonian: no Hamiltonian cycle but path sets exist;
	// the expected count comes from the oracle (see NOTES on the spec claim).
	{name: "Petersen", n: 10, edges: petersenEdges()},
	{name: "K_5 reversed order", n: 5, edges: completeGraph(5), order: []int{9, 7, 5, 3, 1, 0, 2, 4, 6, 8}, want: u64(60)},
	{name: "C_6 shifted order", n: 6, edges: cycleGraph(6), order: []int{4, 0, 3, 5, 1, 2}, want: u64(6)},

	// Cycles family: closed formulas are (n−1)!/2 for K_n and 1 for C_n.
	{name: "cycles P_5", fam: Cycles, n: 5, edges: pathGraph(5), want: u64(0)},
	{name: "cycles P_2", fam: Cycles, n: 2, edges: pathGraph(2), want: u64(0)},
	{name: "cycles C_3=K_3", fam: Cycles, n: 3, edges: cycleGraph(3), want: u64(1)},
	{name: "cycles C_6", fam: Cycles, n: 6, edges: cycleGraph(6), want: u64(1)},
	{name: "cycles K_3", fam: Cycles, n: 3, edges: completeGraph(3), want: u64(1)},
	{name: "cycles K_4", fam: Cycles, n: 4, edges: completeGraph(4), want: u64(3)},
	{name: "cycles K_5", fam: Cycles, n: 5, edges: completeGraph(5), want: u64(12)},
	{name: "cycles star K_1,4", fam: Cycles, n: 5, edges: starEdges(4), want: u64(0)},
	{name: "cycles two triangles", fam: Cycles, n: 6, edges: [][2]int{{0, 1}, {1, 2}, {0, 2}, {3, 4}, {4, 5}, {3, 5}}, want: u64(0)},
	{name: "cycles isolated vertex", fam: Cycles, n: 3, edges: [][2]int{{0, 1}}, want: u64(0)},
	// Petersen: hypohamiltonian — zero cycle sets (the spec's key control),
	// the paths counterpart above pins the nonzero side.
	{name: "cycles Petersen", fam: Cycles, n: 10, edges: petersenEdges(), want: u64(0)},
	{name: "cycles K_5 reversed order", fam: Cycles, n: 5, edges: completeGraph(5), order: []int{9, 7, 5, 3, 1, 0, 2, 4, 6, 8}, want: u64(12)},
}

// TestFamilyMatchesOracle checks Count() and the enumerated family of every
// case against the brute-force oracle (and explicit formula counts where the
// spec provides them).
func TestFamilyMatchesOracle(t *testing.T) {
	for _, tc := range familyCases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Build(tc.n, tc.edges, tc.fam, tc.order)
			require.NoError(t, err)

			wantSets := hamiltonianSets(t, tc.n, tc.edges, tc.fam)
			if tc.want == nil {
				// Formula-free cases (Petersen paths) still pin a nonempty family.
				assert.NotEmpty(t, wantSets, "oracle-only case must enumerate a nonempty family")
			}
			assert.Equal(t, uint64(len(wantSets)), d.Count(), "count vs brute force")
			if tc.want != nil {
				assert.Equal(t, *tc.want, d.Count(), "count vs formula")
			}

			var got [][]int
			for set := range d.All() {
				got = append(got, set)
			}
			assert.ElementsMatch(t, wantSets, got, "All() vs brute force")
		})
	}
}

// TestEmptyDiagramIsZeroTerminal checks the empty-family shape: an isolated
// vertex leaves only the terminals in the arena and All yields nothing.
func TestEmptyDiagramIsZeroTerminal(t *testing.T) {
	d, err := Build(3, [][2]int{{0, 1}}, Paths, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 0, d.Count())
	assert.Equal(t, 2, d.LiveNodes(), "arena holds just the two terminals")
	assert.Equal(t, 0, d.LevelPeak(), "no non-terminal nodes on any level")
	for range d.All() {
		t.Fatal("empty family must not yield sets")
	}
}

// TestDeterminism builds the same inputs twice and requires identical diagrams.
func TestDeterminism(t *testing.T) {
	cases := []struct {
		name  string
		edges [][2]int
		order []int
		n     int
	}{
		{name: "K_6", n: 6, edges: completeGraph(6)},
		{name: "K_5 permuted", n: 5, edges: completeGraph(5), order: []int{9, 7, 5, 3, 1, 0, 2, 4, 6, 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d1, err := Build(tc.n, tc.edges, Paths, tc.order)
			require.NoError(t, err)
			d2, err := Build(tc.n, tc.edges, Paths, tc.order)
			require.NoError(t, err)
			assert.Equal(t, d1.LiveNodes(), d2.LiveNodes())
			assert.Equal(t, d1.LevelPeak(), d2.LevelPeak())
			assert.Equal(t, d1.Count(), d2.Count())
		})
	}
}

// markReachable marks every node reachable from id (terminals are leaves).
func markReachable(arena []znode, id uint32, seen map[uint32]bool) map[uint32]bool {
	if seen == nil {
		seen = make(map[uint32]bool)
	}
	if id == idNil || seen[id] {
		return seen
	}
	seen[id] = true
	if id > idOne {
		markReachable(arena, arena[id].low, seen)
		markReachable(arena, arena[id].high, seen)
	}
	return seen
}

// --- input validation ---------------------------------------------------------

func TestBuildValidation(t *testing.T) {
	cases := []struct {
		name    string
		wantErr string
		edges   [][2]int
		order   []int
		fam     Family
		n       int
	}{
		{fam: Family(7), n: 2, edges: pathGraph(2), wantErr: "unknown family"},
		{name: "zero vertices", n: 0, wantErr: "nVerts"},
		{name: "negative vertices", n: -3, wantErr: "nVerts"},
		{name: "too many vertices", n: maxVerts + 1, wantErr: "exceeds"},
		{name: "endpoint out of range", n: 2, edges: [][2]int{{0, 2}}, wantErr: "outside"},
		{name: "negative endpoint", n: 2, edges: [][2]int{{-1, 0}}, wantErr: "outside"},
		{name: "loop", n: 3, edges: [][2]int{{1, 1}}, wantErr: "loop"},
		{name: "duplicate edge", n: 3, edges: [][2]int{{0, 1}, {0, 1}}, wantErr: "duplicate"},
		{name: "reversed duplicate", n: 3, edges: [][2]int{{0, 1}, {1, 0}}, wantErr: "duplicate"},
		{name: "order too short", n: 3, edges: [][2]int{{0, 1}, {1, 2}}, order: []int{0}, wantErr: "order length"},
		{name: "order not permutation", n: 3, edges: [][2]int{{0, 1}, {1, 2}}, order: []int{0, 0}, wantErr: "permutation"},
		{name: "order out of range", n: 3, edges: [][2]int{{0, 1}, {1, 2}}, order: []int{0, 5}, wantErr: "permutation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.n, tc.edges, tc.fam, tc.order)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestReductionInvariants audits the built arena: no (variable, low, high)
// duplicates, no node with a zero-terminal high branch, no node unreachable
// from the root (specs/zdd.md «Аудит редукции»).
func TestReductionInvariants(t *testing.T) {
	audited := []struct {
		edges [][2]int
		fam   Family
		n     int
	}{
		{n: 4, edges: completeGraph(4)},
		{n: 6, edges: cycleGraph(6)},
		{fam: Cycles, n: 4, edges: completeGraph(4)},
		{fam: Cycles, n: 5, edges: completeGraph(5)},
	}
	for _, tc := range audited {
		d, err := Build(tc.n, tc.edges, tc.fam, nil)
		require.NoError(t, err)

		keys := make(map[string]uint32, len(d.arena))
		reachable := markReachable(d.arena, d.root, nil)

		for id := uint32(numTerminals); id < uint32(len(d.arena)); id++ {
			n := &d.arena[id]
			assert.True(t, reachable[id], "node %d unreachable from root", id)
			assert.NotEqual(t, idZero, n.high, "zero-suppression violated at node %d", id)
			key := fmt.Sprintf("%d/%d/%d", n.variable, n.low, n.high)
			assert.NotContains(t, keys, key, "duplicate (variable, low, high): %s", key)
			keys[key] = id
		}
	}
}
