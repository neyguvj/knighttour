package shapecount_test

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"knighttour/graph"
	"knighttour/pruner"
	"knighttour/state"
)

// TestShapeFeasibilityClassification is the plan-02 stage-0 tool: it reads a
// zero-shape dump produced by counter.TestDumpZeroShapes ("<shape-hex>
// <endmask-hex>" per line, every listed shape has h == 0 for all ends) and
// attributes every zero shape to the first class that explains it:
//
//	dead-at-root     — current ShouldPruneAfterVisit already kills every end
//	                   at the DP root (class 1, already free);
//	articulation-3+  — additionally needed: articulation cut per end (class 2);
//	endpoint-mismatch— additionally needed: degree-1 analysis of G[shape] (3);
//	forced-cycle     — additionally needed: forced-chain cut per end (4);
//	deep             — structurally alive, the zero only shows after a full
//	                   descent (5).
//
// The union coverage (classes 2–4 share) is the upper bound of any such
// filter's gain. Gated by env: SHAPE_CLASSIFY=<dump file> and
// SHAPE_CLASSIFY_SIZE=<board size used to normalize the dump>.
func TestShapeFeasibilityClassification(t *testing.T) {
	path := os.Getenv("SHAPE_CLASSIFY")
	if path == "" {
		t.Skip("set SHAPE_CLASSIFY=<dump file> (see counter.TestDumpZeroShapes)")
	}
	size, err := strconv.Atoi(os.Getenv("SHAPE_CLASSIFY_SIZE"))
	require.NoError(t, err, "SHAPE_CLASSIFY_SIZE is required")

	g := graph.New(size)
	p := pruner.New(g)

	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	var (
		shapes  int64
		classes [5]int64 // dead-at-root, articulation, endpoint-mismatch, forced-cycle, deep
		// Shape-level cumulative coverage (all ends killed): root∨chain,
		// root∨articulation, root∨chain∨articulation, root∨A∨B∨C.
		uB, uC, uBC, union int64
		// Per-end coverage beyond the root L1 pass.
		endsTotal, endsKilledA, endsKilledB, endsKilledC int64
	)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		require.Len(t, fields, 2, "line %q", line)
		v, err := strconv.ParseUint(fields[0], 16, 64)
		require.NoError(t, err)
		e, err := strconv.ParseUint(fields[1], 16, 64)
		require.NoError(t, err)
		shape, endMask := state.State(v), state.State(e)

		shapes++
		allRoot, allA, allB, allC, allBC, allABC := true, true, true, true, true, true
		for end := range endMask.AllVisited() {
			todo := shape.Unvisit(end)
			killedRoot, _ := p.ShouldPruneAfterVisit(end, todo)

			_, reasonA := p.ShapeFeasible(end, shape, pruner.L2Endpoints)
			_, reasonB := p.ShapeFeasible(end, shape, pruner.L2ForcedChain)
			_, reasonC := p.ShapeFeasible(end, shape, pruner.L2Articulation)
			killedA := killedRoot || reasonA != pruner.NoReason
			killedB := killedRoot || reasonB != pruner.NoReason
			killedC := killedRoot || reasonC != pruner.NoReason

			endsTotal++
			if !killedRoot {
				if reasonA != pruner.NoReason {
					endsKilledA++
				}
				if reasonB == pruner.ForcedChain {
					endsKilledB++
				}
				if reasonC == pruner.Articulation {
					endsKilledC++
				}
			}
			allRoot = allRoot && killedRoot
			allA = allA && killedA
			allB = allB && killedB
			allC = allC && killedC
			allBC = allBC && (killedB || killedC)
			allABC = allABC && (killedA || killedB || killedC)
		}

		switch {
		case allRoot:
			classes[0]++
		case allC:
			classes[1]++
		case allA:
			classes[2]++
		case allB:
			classes[3]++
		default:
			classes[4]++
		}
		if allB {
			uB++
		}
		if allC {
			uC++
		}
		if allBC {
			uBC++
		}
		if allABC {
			union++
		}
	}
	require.NoError(t, sc.Err())

	pct := func(n int64) float64 {
		if shapes == 0 {
			return 0
		}
		return 100 * float64(n) / float64(shapes)
	}
	names := [...]string{"dead-at-root", "articulation-3+", "endpoint-mismatch", "forced-cycle", "deep"}
	t.Logf("zero shapes classified: %d (board %d×%d)", shapes, size, size)
	for i, n := range classes {
		t.Logf("  %-18s %10d  %5.1f%%", names[i], n, pct(n))
	}
	killedByFilters := classes[1] + classes[2] + classes[3]
	t.Logf("  gain bound (classes 2-4, killable before DP): %d (%.1f%% of zeros), deep residue: %.1f%%",
		killedByFilters, pct(killedByFilters), pct(classes[4]))
	t.Logf("  shape coverage all-ends-killed: root∨chain(rootB) %.1f%%, root∨artic %.1f%%, root∨chain∨artic %.1f%%, +A %.1f%%",
		pct(uB), pct(uC), pct(uBC), pct(union))
	if endsTotal > 0 {
		ep := func(n int64) float64 { return 100 * float64(n) / float64(endsTotal) }
		t.Logf("  per-end kills beyond root L1: A(endpoint) %.1f%%, B(chain) %.1f%%, C(articulation) %.1f%% of %d ends",
			ep(endsKilledA), ep(endsKilledB), ep(endsKilledC), endsTotal)
	}
}
