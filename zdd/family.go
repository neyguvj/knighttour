package zdd

// Family selects which Hamiltonian edge-set family Build represents.
type Family int

const (
	// Paths: Hamiltonian paths — degrees ≤ 2, connected support, exactly two
	// vertices of degree 1.
	Paths Family = iota
	// Cycles: Hamiltonian cycles — every vertex has degree exactly 2 and the
	// support is a single cycle.
	Cycles
)

// valid reports whether fam is one of the defined families.
func (fam Family) valid() bool { return fam == Paths || fam == Cycles }

// acceptsTerminal reports whether the state at the terminal level denotes a
// member of the family: for Paths exactly two vertices closed with degree 1
// (cover and acyclicity then force a single spanning path); for Cycles the
// absorbing sealed flag says the one cycle completed (strict cover — every
// vertex closes at degree exactly 2 — already excluded every other shape).
func (fam Family) acceptsTerminal(st *state) bool {
	if fam == Cycles {
		return st.sealed
	}
	return st.ends == 2
}

// parityEmptyCycles reports whether the graph provably admits no Hamiltonian
// cycle by the bipartite parity criterion: a connected bipartite graph whose
// color classes differ in size has no cycle covering all vertices (the knight
// boards of odd side are exactly this case). No claim — false — for
// disconnected or non-bipartite graphs; the full build decides those.
func parityEmptyCycles(nVerts int, edges [][2]int) bool {
	const uncolored = int8(-1)
	adj := adjacency(nVerts, edges)
	color := make([]int8, nVerts)
	for v := range color {
		color[v] = uncolored
	}
	count := [2]int{}
	queue := []int{0}
	color[0] = 0
	count[0]++
	reached := 1
	for len(queue) > 0 {
		v := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, w := range adj[v] {
			if color[w] != uncolored {
				if color[w] == color[v] {
					return false // not bipartite: no parity claim
				}
				continue
			}
			color[w] = 1 - color[v]
			count[color[w]]++
			reached++
			queue = append(queue, int(w))
		}
	}
	if reached < nVerts {
		return false // disconnected: no parity claim (cover decides)
	}
	return count[0] != count[1]
}

// adjacency returns the undirected neighbor lists of the graph; built once
// per call, used by the parity shortcut only (off the hot build path).
func adjacency(nVerts int, edges [][2]int) [][]int32 {
	adj := make([][]int32, nVerts)
	for _, e := range edges {
		adj[e[0]] = append(adj[e[0]], int32(e[1]))
		adj[e[1]] = append(adj[e[1]], int32(e[0]))
	}
	return adj
}
