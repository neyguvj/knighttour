package types

import (
	"testing"

	"knighttour/pruner"

	"github.com/stretchr/testify/assert"
)

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
		{name: "forced chain", reason: pruner.ForcedChain, fieldOf: func(r *Result) int { return r.PrunedForcedChain }},
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
		r.CountPrune(pruner.ForcedChain)
		r.Finalize()
		assert.Equal(t, 4, r.Pruned)
	})
}
