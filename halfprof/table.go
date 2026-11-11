package halfprof

// table.go — in-memory aggregation of profile counts with the soft byte
// budget attached. The first refused growth latches a sticky error; later
// adds become no-ops so the DFS unwinds instead of looping on failures.

import "errors"

// profileSink receives the aggregated profile counts one half enumeration
// produces. add folds one (key, weight) observation in; failure reports a
// sticky error that should stop the enumeration (a soft-cap breach or an I/O
// fault). Both the in-memory aggTable and the disk-spilling spillTable satisfy
// it, so enum.go drives either unchanged.
type profileSink interface {
	add(k profKey, w uint64)
	failure() error
}

// aggTable accumulates weight per profile key under a shared memBudget.
type aggTable struct {
	err  error
	m    map[profKey]uint64
	bud  *memBudget
	side string
}

// newAggTable returns an empty table charging growth to bud; side names the
// table in cap errors ("upper"/"lower").
func newAggTable(side string, bud *memBudget) *aggTable {
	return &aggTable{side: side, m: make(map[profKey]uint64), bud: bud}
}

// add accumulates w into key k, reserving per-entry bytes for new profiles.
func (t *aggTable) add(k profKey, w uint64) {
	if t.err != nil {
		return
	}
	if _, ok := t.m[k]; ok {
		t.m[k] += w
		return
	}
	if err := t.bud.reserve(entryReserve); err != nil {
		var hit *capHit
		if errors.As(err, &hit) {
			t.err = &OverCapError{Side: t.side, Entries: len(t.m) + 1, Bytes: int(hit.need), Cap: softCap}
		} else {
			t.err = err
		}
		return
	}
	t.m[k] = w
}

// len reports the number of distinct profiles held.
func (t *aggTable) len() int { return len(t.m) }

// failure returns the sticky cap error, if any, so enum can stop on it.
func (t *aggTable) failure() error { return t.err }

// aggTable is a profileSink.
var _ profileSink = (*aggTable)(nil)
