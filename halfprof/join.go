package halfprof

// join.go — merging the two profile tables: entries are sorted by (mask,
// pairing), equal-mask runs are crossed, and every candidate pair is decided
// by the alternating-links walk (the cc = 0 test). The accepted products sum
// into the group count. ctx is polled at every mask-run boundary and every
// joinTick candidate pairs inside a run.

import (
	"cmp"
	"context"
	"slices"
)

// joinTick bounds how often the merge loop polls ctx between run boundaries:
// one check per this many candidate pair tests, keeping the poll off the hot
// inner loop while bounding cancellation latency.
const joinTick = 1 << 20

// entry is one table row prepared for the merge join.
type entry struct {
	k profKey
	w uint64
}

// sortedEntries flattens and orders a table by mask then pairing bytes, the
// order the run-merge consumes.
func sortedEntries(m map[profKey]uint64) []entry {
	out := make([]entry, 0, len(m))
	for k, w := range m {
		out = append(out, entry{k: k, w: w})
	}
	slices.SortFunc(out, func(a, b entry) int {
		if c := cmp.Compare(a.k.K, b.k.K); c != 0 {
			return c
		}
		return cmp.Compare(a.k.P, b.k.P)
	})
	return out
}

// joinTables merges the sorted tables and returns the tour count of the group
// (sum of accepted weight products) and the number of accepted pairs. A done
// ctx aborts with its error.
func joinTables(ctx context.Context, u, l []entry) (total, joins uint64, err error) {
	i, j := 0, 0
	for i < len(u) && j < len(l) {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		switch {
		case u[i].k.K < l[j].k.K:
			i = runEnd(u, i)
		case u[i].k.K > l[j].k.K:
			j = runEnd(l, j)
		default:
			i2, j2 := runEnd(u, i), runEnd(l, j)
			t, n, err := crossRuns(ctx, u[i:i2], l[j:j2])
			if err != nil {
				return 0, 0, err
			}
			total, joins = total+t, joins+n
			i, j = i2, j2
		}
	}
	return total, joins, nil
}

// crossRuns tests every pairing combination of two equal-mask runs, adding
// accepted products to total and counting them in joins. The lower run is
// decoded once per call: the cross product would otherwise decode it again
// for every upper entry.
func crossRuns(ctx context.Context, ru, rl []entry) (total, joins uint64, err error) {
	qs := make([][]int8, len(rl))
	for i, b := range rl {
		qs[i] = decodePairing(b.k.P)
	}
	pairs := 0
	for _, a := range ru {
		p := decodePairing(a.k.P)
		m := len(p) - 2
		for i, q := range qs {
			if pairs&(joinTick-1) == 0 {
				if err := ctx.Err(); err != nil {
					return 0, 0, err
				}
			}
			pairs++
			if singlePathOK(p, q, m) {
				total += a.w * rl[i].w
				joins++
			}
		}
	}
	return total, joins, nil
}

// runEnd returns the index after the equal-K run starting at i.
func runEnd(e []entry, i int) int {
	j := i + 1
	for j < len(e) && e[j].k.K == e[i].k.K {
		j++
	}
	return j
}
