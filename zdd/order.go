package zdd

// narrowPasses bounds the deterministic insertion-move local search that
// refines the best candidate order (each pass costs O(m²) width evaluations).
const narrowPasses = 4

// OrderNarrow returns a deterministic edge order (a permutation of [0, m))
// chosen for small frontier cut width: greedy sweep orders seeded at every
// vertex plus the row order and its reverse are ranked by their exact cut
// width, and the winner is refined by insertion moves that strictly reduce
// it. Ties break by candidate generation order, so the same graph yields the
// same permutation in every process. OrderNarrow does not validate the edge
// list (Build does); any input yields a valid permutation of [0, m).
func OrderNarrow(nVerts int, edges [][2]int) []int {
	m := len(edges)
	best := make([]int, m)
	for i := range best {
		best[i] = i
	}
	if m < 3 {
		return best // every order of ≤ 2 edges has the same width
	}
	bestW := cutWidth(nVerts, edges, best)

	rev := make([]int, m)
	for i := range rev {
		rev[i] = m - 1 - i
	}
	if w := cutWidth(nVerts, edges, rev); w < bestW {
		best, bestW = rev, w
	}

	for seed := range nVerts {
		start := firstIncidentEdge(seed, edges)
		if start < 0 {
			continue // isolated vertex: no sweep starts here
		}
		cand := greedyNarrowOrder(nVerts, edges, start)
		if w := cutWidth(nVerts, edges, cand); w < bestW {
			best, bestW = cand, w
		}
	}
	return improveCutWidth(nVerts, edges, best, bestW)
}

// firstIncidentEdge returns the lowest edge index incident to v, or −1 when v
// has no incident edge. The scan is linear in the edge list, so the result is
// deterministic and never confuses a neighbour vertex id with an edge index.
func firstIncidentEdge(v int, edges [][2]int) int {
	for k, e := range edges {
		if e[0] == v || e[1] == v {
			return k
		}
	}
	return -1
}

// cutWidth returns the exact frontier width of the order: the maximum over
// all levels of the number of vertices incident to both a decided and an
// undecided edge (first[v] < level ≤ last[v]).
func cutWidth(nVerts int, edges [][2]int, ord []int) int {
	m := len(ord)
	first := make([]int, nVerts)
	last := make([]int, nVerts)
	for v := range first {
		first[v] = m
		last[v] = -1
	}
	for p, k := range ord {
		for _, x := range edges[k] {
			if first[x] > p {
				first[x] = p
			}
			if last[x] < p {
				last[x] = p
			}
		}
	}
	delta := make([]int, m+1)
	for v := range nVerts {
		if last[v] >= 0 {
			delta[first[v]+1]++
			delta[last[v]+1]--
		}
	}
	cur, peak := 0, 0
	for i := range m + 1 {
		cur += delta[i]
		peak = max(peak, cur)
	}
	return peak
}

// greedyNarrowOrder grows an order edge by edge starting from the edge start,
// the lowest-index edge of a seed vertex. Each next edge minimizes,
// lexicographically: new vertices born into the frontier, then (negated)
// vertices dying with this take, then original edge index — so the sweep hugs
// a corridor instead of spreading the boundary, and ties never depend on
// iteration order.
func greedyNarrowOrder(nVerts int, edges [][2]int, start int) []int {
	m := len(edges)
	rem := make([]int, nVerts)
	for _, e := range edges {
		rem[e[0]]++
		rem[e[1]]++
	}
	born := make([]bool, nVerts)
	used := make([]bool, m)
	ord := make([]int, 0, m)

	pick := start
	for range m {
		if pick >= 0 {
			ord = append(ord, pick)
			used[pick] = true
			for _, x := range edges[pick] {
				born[x] = true
				rem[x]--
			}
		}
		pick = nextGreedyEdge(edges, used, born, rem)
	}
	return ord
}

// nextGreedyEdge scans the unused edges and returns the best one under the
// (born, dying, index) key of greedyNarrowOrder, or −1 when none are left.
func nextGreedyEdge(edges [][2]int, used, born []bool, rem []int) int {
	best, bestNew, bestDie := -1, 3, -1
	for k := range edges {
		if used[k] {
			continue
		}
		nb, die := 0, 0
		for _, x := range edges[k] {
			if !born[x] {
				nb++
			} else if rem[x] == 1 {
				die++
			}
		}
		if nb < bestNew || (nb == bestNew && die > bestDie) {
			best, bestNew, bestDie = k, nb, die
		}
	}
	return best
}

// improveCutWidth applies best-improvement insertion moves (take the edge at
// one position, reinsert it at another) while the width strictly drops, for a
// bounded number of passes; all loops are index-based, so the outcome is
// deterministic.
func improveCutWidth(nVerts int, edges [][2]int, ord []int, w int) []int {
	m := len(ord)
	for range min(narrowPasses, m) {
		bestP, bestQ, bestW := -1, -1, w
		for p := range m {
			for q := range m {
				if q == p {
					continue
				}
				cand := movedCopy(ord, p, q)
				if cw := cutWidth(nVerts, edges, cand); cw < bestW {
					bestP, bestQ, bestW = p, q, cw
				}
			}
		}
		if bestP < 0 {
			break
		}
		ord, w = movedCopy(ord, bestP, bestQ), bestW
	}
	return ord
}

// movedCopy returns the permutation of ord with the entry at p removed and
// reinserted at index q (clamped to the shortened slice), shifting the rest.
func movedCopy(ord []int, p, q int) []int {
	cand := make([]int, 0, len(ord))
	for i, v := range ord {
		if i != p {
			cand = append(cand, v)
		}
	}
	pos := min(q, len(cand))
	cand = append(cand, 0)
	copy(cand[pos+1:], cand[pos:])
	cand[pos] = ord[p]
	return cand
}
