package types

import (
	"testing"

	"knighttour/pruner"

	"github.com/stretchr/testify/assert"
)

func TestResultAdd(t *testing.T) {
	r := Result{TotalPathsFound: 5, CacheWrites: 3, CacheHits: 10, CacheMisses: 2, PrunedDeadEnd: 4}
	other := Result{TotalPathsFound: 7, CacheWrites: 1, CacheHits: 5, CacheMisses: 8, PrunedDisconn: 6}

	r.Add(other)

	assert.Equal(t, Result{
		TotalPathsFound: 12,
		CacheWrites:     4,
		CacheHits:       15,
		CacheMisses:     10,
		PrunedDeadEnd:   4,
		PrunedDisconn:   6,
	}, r)
}

func TestResultCountPrune(t *testing.T) {
	tests := []struct {
		fieldOf func(*Result) int
		name    string
		reason  pruner.Reason
	}{
		{name: "dead end", reason: pruner.DeadEnd, fieldOf: func(r *Result) int { return r.PrunedDeadEnd }},
		{name: "no continuation", reason: pruner.NoContinuation, fieldOf: func(r *Result) int { return r.PrunedNoCont }},
		{name: "disconnected", reason: pruner.Disconnected, fieldOf: func(r *Result) int { return r.PrunedDisconn }},
		{name: "endpoints", reason: pruner.Endpoints, fieldOf: func(r *Result) int { return r.PrunedEndpoints }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var r Result
			r.CountPrune(pruner.NoReason)
			r.Finalize()
			assert.Zero(t, r.Pruned, "NoReason must not be counted")

			r.CountPrune(tc.reason)
			r.CountPrune(tc.reason)
			assert.Equal(t, 2, tc.fieldOf(&r))

			r.Finalize()
			assert.Equal(t, 2, r.Pruned, "Finalize aggregates the breakdown")
		})
	}

	t.Run("mixed reasons sum up", func(t *testing.T) {
		var r Result
		r.CountPrune(pruner.DeadEnd)
		r.CountPrune(pruner.Endpoints)
		r.CountPrune(pruner.Disconnected)
		r.Finalize()
		assert.Equal(t, 3, r.Pruned)
	})
}
