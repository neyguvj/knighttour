// Package halfprof counts open knight's tours by the half-profile join of
// Chernov and McKay: the board is cut in two horizontal halves, each half is
// enumerated as spanning linear forests keyed by a seam profile (defect mask
// plus pairings of the seam stubs), and the tables are merged — a pair of
// half-configs forms a tour exactly when the alternating walk over the two
// pairings covers every stub once from ⊥s to ⊥t. Counts are per unordered
// endpoint pair {s,t} as undirected edge sets; the board total sums the D4
// orbit sizes times the group counts (specs/halfprof.md).
package halfprof

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sort"

	"knighttour/symmetry"
	"knighttour/workerpool"
)

// Group is one endpoint pair of a D4 orbit: S and T are cell indices
// (row-major), K the orbit size — how many board pairs share its count.
type Group struct {
	S, T, K int
}

// Groups returns the endpoint-pair orbits of an n×n board under the eight
// dihedral symmetries, sorted by (S,T); each S < T is the lexicographic
// minimum of its orbit and K ∈ {2,4,8} the number of distinct images. Out of
// contract n (< 5 or > 8) yields nil.
func Groups(n int) []Group {
	if n < 5 || n > 8 {
		return nil
	}
	sym := symmetry.NewSymmetry(n)
	total := n * n
	size := make(map[[2]int]int)
	for a := range total - 1 {
		for b := a + 1; b < total; b++ {
			seen := make(map[[2]int]bool, 8)
			best := [2]int{total, total}
			for t := range 8 {
				ta, tb := sym.TransformCell(uint8(t), a), sym.TransformCell(uint8(t), b)
				p := [2]int{min(ta, tb), max(ta, tb)}
				seen[p] = true
				if p[0] < best[0] || p[0] == best[0] && p[1] < best[1] {
					best = p
				}
			}
			size[best] = len(seen)
		}
	}
	out := make([]Group, 0, len(size))
	for p, k := range size {
		out = append(out, Group{S: p[0], T: p[1], K: k})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].S < out[j].S || out[i].S == out[j].S && out[i].T < out[j].T
	})
	return out
}

// Census carries the profile statistics of one group run: the live sizes of
// the two half tables, the number of accepted profile pairs (Joins) and the
// largest single-profile counter across both tables (MaxProfileCount).
type Census struct {
	ProfilesU       int
	ProfilesL       int
	Joins           uint64
	MaxProfileCount uint64
}

// CountGroup returns the number of open tours between endpoints s and t as
// undirected edge sets. Invalid n or cells and ctx termination return errors;
// an aggregation crossing the soft cap returns an ErrOverCap error.
func CountGroup(ctx context.Context, n, s, t int) (uint64, error) {
	total, _, err := countGroupRun(ctx, n, s, t)
	return total, err
}

// CountGroupWithCensus is CountGroup plus the profile statistics of the run.
func CountGroupWithCensus(ctx context.Context, n, s, t int) (uint64, Census, error) {
	return countGroupRun(ctx, n, s, t)
}

// CountGroupWithPasses counts the group in Chernov's multi-pass bucket mode:
// passes independent passes 0..passes-1, each enumerating both halves and
// keeping only its mask bucket; the total is the sum of the partial per-pass
// totals in fixed order (specs/halfprof.md). passes ≥ 1 is the caller contract
// (below it returns an error); passes == 1 equals CountGroupWithCensus. The
// Census aggregates the passes: profile and join counts summed, MaxProfileCount
// the max over passes. A pass whose tables cross the soft cap returns an error
// hinting to raise passes, still matching ErrOverCap via errors.Is/As.
func CountGroupWithPasses(ctx context.Context, n, s, t, passes int) (uint64, Census, error) {
	return countGroupPasses(ctx, n, s, t, passes)
}

// CountBoard returns the total number of open tours on an n×n board as
// directed paths (each edge set counted in both orientations), summing
// K·count over the endpoint orbits with a crew of workers. The result is
// independent of workers; a terminated ctx returns ctx.Err().
func CountBoard(ctx context.Context, n, workers int) (uint64, error) {
	return countBoardPasses(ctx, n, workers, 1)
}

// CountBoardWithPasses is CountBoard with each group counted in the multi-pass
// bucket mode at the given passes; the caller owns the passes policy.
func CountBoardWithPasses(ctx context.Context, n, workers, passes int) (uint64, error) {
	return countBoardPasses(ctx, n, workers, passes)
}

// countBoardPasses sums K·count over the endpoint orbits with a worker crew,
// counting every group with `passes` bucket passes.
func countBoardPasses(ctx context.Context, n, workers, passes int) (uint64, error) {
	if err := validateN(n); err != nil {
		return 0, err
	}
	groups := Groups(n)
	counts := make([]uint64, len(groups))
	errs := make([]error, len(groups))
	pool := workerpool.New(max(workers, 1))
	pool.Run(ctx, len(groups), func(ctx context.Context, idx iter.Seq[int]) {
		for i := range idx {
			counts[i], _, errs[i] = countGroupPasses(ctx, n, groups[i].S, groups[i].T, passes)
		}
	})
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	for _, err := range errs {
		if err != nil {
			return 0, err
		}
	}
	var total uint64
	for i, gr := range groups {
		total += uint64(gr.K) * counts[i]
	}
	return 2 * total, nil
}

// countGroupRun is the single-pass group count (bucket mode at passes = 1).
func countGroupRun(ctx context.Context, n, s, t int) (uint64, Census, error) {
	return countGroupPasses(ctx, n, s, t, 1)
}

// countGroupPasses enumerates both half tables of one endpoint pair over the
// bucket passes and joins each pass; see the package header for the method.
func countGroupPasses(ctx context.Context, n, s, t, passes int) (uint64, Census, error) {
	if err := validateN(n); err != nil {
		return 0, Census{}, err
	}
	if err := validateEndpoints(n, s, t); err != nil {
		return 0, Census{}, err
	}
	if passes < 1 {
		return 0, Census{}, fmt.Errorf("halfprof: passes %d below 1", passes)
	}
	g := newCutGeom(n)

	var (
		total uint64
		cen   Census
	)
	for pass := range passes {
		part, pc, err := countBucketPass(ctx, g, s, t, pass, passes)
		if err != nil {
			return 0, Census{}, capHint(err, pass, passes)
		}
		total += part
		cen.ProfilesU += pc.ProfilesU
		cen.ProfilesL += pc.ProfilesL
		cen.Joins += pc.Joins
		cen.MaxProfileCount = max(cen.MaxProfileCount, pc.MaxProfileCount)
	}
	return total, cen, nil
}

// capHint annotates a soft-cap failure with the failing pass and an instruction
// to raise passes; non-cap errors pass through untouched. The annotation keeps
// ErrOverCap and *OverCapError reachable through errors.Is/As (specs/halfprof.md).
func capHint(err error, pass, passes int) error {
	var oe *OverCapError
	if !errors.As(err, &oe) {
		return err
	}
	return fmt.Errorf("%w: bucket pass %d of %d full, raise passes", err, pass, passes)
}

// countBucketPass runs one bucket pass: enumerates both halves keeping only
// masks of this pass's bucket, joins them and reports the partial total and its
// census. The soft-cap budget spans the pass's two tables jointly.
func countBucketPass(ctx context.Context, g *cutGeom, s, t, pass, passes int) (uint64, Census, error) {
	bud := &memBudget{}

	up := newAggTable("upper", bud)
	if err := runHalf(g, ctx, false, s, t, pass, passes, up); err != nil {
		return 0, Census{}, err
	}
	lo := newAggTable("lower", bud)
	if err := runHalf(g, ctx, true, s, t, pass, passes, lo); err != nil {
		return 0, Census{}, err
	}
	total, joins, err := joinTables(ctx, sortedEntries(up.m), sortedEntries(lo.m))
	if err != nil {
		return 0, Census{}, err
	}
	cen := Census{
		ProfilesU:       up.len(),
		ProfilesL:       lo.len(),
		Joins:           joins,
		MaxProfileCount: maxProfile(up.m, lo.m),
	}
	return total, cen, nil
}

// maxProfile returns the largest single counter across the given tables —
// the width witness for profile counters (plan 30, step 0).
func maxProfile(tables ...map[profKey]uint64) uint64 {
	var best uint64
	for _, t := range tables {
		for _, w := range t {
			if w > best {
				best = w
			}
		}
	}
	return best
}

// runHalf enumerates one half of the board into sink, keeping only masks in
// bucket `pass` of `passes`.
func runHalf(g *cutGeom, ctx context.Context, lower bool, s, t, pass, passes int, sink profileSink) error {
	h := newHalfEnum(g, ctx, lower, s, t, pass, passes, sink)
	return h.run()
}

// validateN checks the board size against the contract range.
func validateN(n int) error {
	if n < 5 || n > 8 {
		return fmt.Errorf("halfprof: board size %d outside [5,8]", n)
	}
	return nil
}

// validateEndpoints checks that s and t are distinct cells of an n×n board.
func validateEndpoints(n, s, t int) error {
	if (s < 0 || s >= n*n || t < 0 || t >= n*n) || s == t {
		return fmt.Errorf("halfprof: endpoints %d,%d invalid for size %d", s, t, n)
	}
	return nil
}
