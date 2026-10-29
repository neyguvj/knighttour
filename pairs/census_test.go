package pairs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"knighttour/graph"
	"knighttour/pruner"
)

// Plan-28 probe anchors: the 6×6 balanced cut m = 18 from the corner enumerates
// 402 530 pruned prefix half-paths (the historical number of plan 28); the side
// table holds 155 370 (mask,end) records (re-measured here, pinned as a
// regression anchor). The suffix total 5 453 628 992 costs hours and stays a
// gated point of BenchmarkPairsProbe/BenchmarkPairsHalfCensus.
const (
	plan28WalksP6x6  int64 = 402_530
	plan28Entries6x6 int64 = 155_370
)

// censusTestHooks returns hooks with a small work tick and an effectively
// disabled wall-clock mark: profiles stay deterministic in node counts.
func censusTestHooks() censusHooks {
	return censusHooks{tickNodes: 1 << 14, interval: time.Hour}
}

// The instrumented descent reproduces Row bit-for-bit and its counters are
// deterministic between runs (plan 29 "Корректность"): the census is an
// observer, not a second algorithm.
func TestRowCensusMatchesRow(t *testing.T) {
	tests := []struct {
		name  string
		size  int
		start int
	}{
		{name: "5x5 corner", size: 5, start: 0},
		{name: "5x5 center", size: 5, start: 12},
		{name: "6x6 corner", size: 6, start: probeStart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := graph.New(tt.size)
			hooks := censusTestHooks()

			hist1, nodes1, leaves1 := rowCensus(context.Background(), g, tt.start, hooks)
			assert.Equal(t, Row(context.Background(), g, tt.start), hist1, "census histogram vs Row")
			assert.Equal(t, sum(hist1), leaves1, "leaves equal the histogram sum")

			hist2, nodes2, leaves2 := rowCensus(context.Background(), g, tt.start, hooks)
			assert.Equal(t, hist1, hist2, "histogram between runs")
			assert.Equal(t, nodes1, nodes2, "node counter between runs")
			assert.Equal(t, leaves1, leaves2, "leaf counter between runs")
		})
	}
}

// The node-kind profile is reproducible: marks fire exactly on power-of-two
// node counts, strictly increasing and identical between runs, and the last
// mark brackets the final counter from below within a factor of two.
func TestRowCensusProfileDeterministic(t *testing.T) {
	g := graph.New(5)

	type mark struct{ nodes, leaves uint64 }
	collect := func() ([]mark, uint64) {
		var got []mark
		_, nodes, _ := rowCensus(context.Background(), g, 0, censusHooks{
			tickNodes: 1 << 14,
			interval:  time.Hour, // no wall-kind marks — the profile is pure work
			emit: func(_ time.Duration, n, l uint64) {
				got = append(got, mark{n, l})
			},
		})
		return got, nodes
	}

	first, nodes := collect()
	second, _ := collect()
	assert.Equal(t, first, second, "profile between runs")

	assert.NotEmpty(t, first)
	for _, m := range first {
		assert.Zerof(t, m.nodes&(m.nodes-1), "mark %d is a power of two", m.nodes)
	}
	last := first[len(first)-1]
	assert.LessOrEqual(t, last.nodes, nodes)
	assert.Greater(t, 2*last.nodes, nodes, "last power-of-two mark brackets the total")
}

// A terminated ctx stops the descent on the next work tick — deterministically
// at exactly tickNodes visited nodes — and yields a partial histogram bounded
// by the full row without panicking.
func TestRowCensusCanceledStopsAtTick(t *testing.T) {
	g := graph.New(5)
	full := Row(context.Background(), g, 0)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	elapsed, stopElapsed := context.WithTimeout(context.Background(), 0)
	defer stopElapsed()

	const tick = uint64(64)
	tests := []struct {
		ctx  context.Context
		name string
	}{
		{name: "canceled", ctx: canceled},
		{name: "deadline elapsed", ctx: elapsed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hist, nodes, leaves := rowCensus(tt.ctx, g, 0, censusHooks{tickNodes: tick, interval: time.Hour})
			assert.Equal(t, tick, nodes, "descent stops exactly on the first tick")
			assert.Equal(t, sum(hist), leaves)
			for c, w := range hist {
				assert.LessOrEqualf(t, w, full[c], "partial cell %d bounded by the full row", c)
			}
		})
	}
}

// The half-path census reproduces the plan-28 probe columns exactly: on 5×5
// live against mitmProbe (prefix side as one walk, suffix side summed over all
// end anchors), and on 6×6 against the historical prefix numbers of the same
// probe — the expensive suffix sum stays behind the -bench filter.
func TestHalfCensusReproducesProbeColumns(t *testing.T) {
	t.Run("5x5_live_vs_probe", func(t *testing.T) {
		g := graph.New(5)
		cells := g.GetTotalCells()
		const m = 13

		_, cen := mitmProbe(context.Background(), g, probeStart, m)

		prefixes := mitmWalk(context.Background(), g, pruner.New(g), probeStart, m)
		assert.Equal(t, cen.walksP, prefixes.walks, "walksP")
		assert.Equal(t, cen.entries, prefixes.records(), "entries")

		var suffixWalks int64
		for end := range cells {
			suffixWalks += mitmWalk(context.Background(), g, nil, end, cells-m).walks
		}
		assert.Equal(t, cen.walksT, suffixWalks, "walksT summed over end anchors")
	})

	t.Run("6x6_prefix_pinned", func(t *testing.T) {
		g := graph.New(6)
		prefixes := mitmWalk(context.Background(), g, pruner.New(g), probeStart, 18)
		assert.Equal(t, plan28WalksP6x6, prefixes.walks, "plan-28 walksP")
		assert.EqualValues(t, plan28Entries6x6, prefixes.records(), "plan-28 side records")
	})
}
