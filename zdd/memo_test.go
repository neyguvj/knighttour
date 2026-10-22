package zdd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Memo-table census tests (plan 24): the counters must be deterministic,
// agree with what their names state on controls decided by construction, and
// stay consistent with each other. The diagram goldens pinned here are the
// cross-step identity guard: every memo-layout change of plan 24 keeps the
// diagram bit-identical, so these numbers may never move.

// censusCase is one census control: an input graph plus the structural facts
// known about it without running the build.
type censusCase struct {
	name       string
	edges      [][2]int
	fam        Family
	n          int
	wantLive   int
	built      bool
	allZero    bool
	wantZeroHt bool
}

// censusCases are the plan-24 controls. The star is dead for cover reasons:
// covering all four leaves needs center degree 4 > 2, so every state result
// is idZero and no state repeats (a linear descent). K_6 mixes live residual
// functions with repeated dead-end states. The skipped controls pin that a
// build that never runs contributes no census at all.
var censusCases = []censusCase{
	{name: "K_6 paths", n: 6, edges: completeGraph(6), built: true, wantLive: 224, wantZeroHt: true},
	{name: "star K_1,4 paths", n: 5, edges: starEdges(4), built: true, wantLive: 2, allZero: true},
	{name: "isolated vertex skipped", n: 3, edges: [][2]int{{0, 1}}},
	{name: "parity-empty cycles skipped", n: 3, edges: starEdges(2), fam: Cycles},
}

// TestMemoCensusCounters pins the memo-table census counters of plan 24 step 1:
// determinism across two builds, zero census for skipped builds, and the
// counter meanings (entries ⊇ zeroEntries; zeroHits only with zero entries) on
// controls whose family is decided by construction.
func TestMemoCensusCounters(t *testing.T) {
	for _, tc := range censusCases {
		t.Run(tc.name, func(t *testing.T) {
			d1, err := Build(tc.n, tc.edges, tc.fam, nil)
			require.NoError(t, err)
			d2, err := Build(tc.n, tc.edges, tc.fam, nil)
			require.NoError(t, err)

			assert.Equal(t, d1.memoEntries, d2.memoEntries, "memoEntries deterministic")
			assert.Equal(t, d1.zeroEntries, d2.zeroEntries, "zeroEntries deterministic")
			assert.Equal(t, d1.zeroHits, d2.zeroHits, "zeroHits deterministic")

			if !tc.built {
				assert.Zero(t, d1.memoEntries, "skipped build must not run the dfs or count anything")
				assert.Zero(t, d1.zeroEntries, "skipped build must not count zero entries")
				assert.Zero(t, d1.zeroHits, "skipped build must not count zero hits")
				return
			}
			assert.Equal(t, tc.wantLive, d1.LiveNodes(), "diagram golden (plan-24 identity guard)")

			assert.Positive(t, d1.memoEntries, "a running build memoizes states")
			assert.LessOrEqual(t, d1.zeroEntries, d1.memoEntries, "zeroEntries is a subset of entries")
			if tc.allZero {
				assert.Equal(t, d1.memoEntries, d1.zeroEntries, "dead family stores only idZero results")
			}
			if tc.wantZeroHt {
				assert.Positive(t, d1.zeroHits, "K_6 revisits dead-end states")
			} else {
				assert.Zero(t, d1.zeroHits, "control without repeated dead states has no zero hits")
			}
		})
	}
}

// memoTestKeyLen is the fixed key length of TestLevelMemoLayout's synthetic keys.
const memoTestKeyLen = 5

// testKey returns a distinct key of memoTestKeyLen bytes for index i.
func testKey(i int) []byte {
	k := make([]byte, memoTestKeyLen)
	for j := range k {
		k[j] = byte(i >> (8 * (j % 8)))
	}
	return k
}

// TestLevelMemoLayout pins the plan-24 lever-A slot layout: stride 4+keyLen
// with no padding, empty marker idNil, capacity doubling while loaded past
// 7/8, ids and keys surviving rehash, per-level key lengths, and a budget sum
// exactly equal to the allocated slot arrays.
func TestLevelMemoLayout(t *testing.T) {
	stride := memoIDBytes + memoTestKeyLen
	mt := newMemoTables(2)

	id, ok := mt.get(0, testKey(0))
	assert.False(t, ok, "unallocated level misses")
	assert.Equal(t, uint32(idNil), id)

	for i := range 200 {
		mt.put(0, testKey(i), uint32(i+1))
	}
	lvl := &mt.levels[0]
	assert.Len(t, lvl.slots, 256*stride, "capacity doubled 64→128→256")
	assert.Equal(t, 200, lvl.count)
	assert.LessOrEqual(t, lvl.count*growLoadDen, len(lvl.slots)/stride*growLoadNum, "load stays ≤ 7/8")
	for i := range 200 {
		got, found := mt.get(0, testKey(i))
		assert.True(t, found, "entry %d survives growth", i)
		assert.Equal(t, uint32(i+1), got)
	}

	// Dispatch is by level: a key stored at level 0 must miss at another one.
	id, ok = mt.get(1, testKey(7))
	assert.False(t, ok, "a stored key misses on another level")
	assert.Equal(t, uint32(idNil), id)

	mt.put(1, []byte{9}, idOne) // a level of its own key length
	assert.Equal(t, 1, mt.levels[1].keyLen)
	assert.Len(t, mt.levels[1].slots, memoStartSlots*(memoIDBytes+1))

	assert.Equal(t, int64(len(lvl.slots)+len(mt.levels[1].slots)), mt.bytes, "budget = allocated arrays")
	assert.Equal(t, 201, mt.memoEntries)
}

// TestMemoEntriesGolden5x5 pins the full 5×5 knight-board census goldens:
// entries and zero counters of both edge orders reproduce ADR-040's numbers
// exactly, and two builds yield identical diagrams. This is the lever-A guard
// that the per-level layout stores the same records as the old global table.
func TestMemoEntriesGolden5x5(t *testing.T) {
	n, edges := knightEdges(t, 5)
	cases := []struct {
		name        string
		order       []int
		wantEntries int
		wantZeroE   int
		wantZeroH   int
		wantLive    int
	}{
		{name: "row", wantEntries: 1_505_611, wantZeroE: 1_494_871, wantZeroH: 234_013, wantLive: 2097},
		{name: "narrow", order: OrderNarrow(n, edges), wantEntries: 31_650, wantZeroE: 27_941, wantZeroH: 6_780, wantLive: 1208},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d1, err := Build(n, edges, Paths, tc.order)
			require.NoError(t, err)
			d2, err := Build(n, edges, Paths, tc.order)
			require.NoError(t, err)

			assert.Equal(t, tc.wantEntries, d1.memoEntries, "memo entries vs ADR-040 golden")
			assert.Equal(t, tc.wantZeroE, d1.zeroEntries, "zero entries vs golden")
			assert.Equal(t, tc.wantZeroH, d1.zeroHits, "zero hits vs golden")
			assert.Equal(t, tc.wantLive, d1.LiveNodes(), "live nodes vs ADR-040 golden")
			assert.Equal(t, d1, d2, "two builds yield identical diagrams")
		})
	}
}
