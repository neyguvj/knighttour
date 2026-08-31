package main

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"knighttour/counter"
	"knighttour/monitoring"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		expected *appArgs
		name     string
		args     []string
		wantErr  bool
	}{
		{
			name:     "defaults",
			args:     nil,
			expected: &appArgs{size: 5, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth},
		},
		{
			name:     "explicit flags",
			args:     []string{"-size", "6", "-workers", "4", "-precompute-depth", "3"},
			expected: &appArgs{size: 6, workers: 4, precomputeDepth: 3},
		},
		{name: "board 5", args: []string{"-size", "5"}, expected: &appArgs{size: 5, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth}},
		{name: "board 6", args: []string{"-size", "6"}, expected: &appArgs{size: 6, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth}},
		{name: "board 7", args: []string{"-size", "7"}, expected: &appArgs{size: 7, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth}},
		{name: "board 8 max depth", args: []string{"-size", "8", "-precompute-depth", "32"}, expected: &appArgs{size: 8, workers: runtime.NumCPU(), precomputeDepth: 32}},
		{name: "size too small", args: []string{"-size", "4"}, wantErr: true},
		{name: "size too large", args: []string{"-size", "9"}, wantErr: true},
		{name: "depth zero", args: []string{"-size", "5", "-precompute-depth", "0"}, wantErr: true},
		{name: "depth above limit", args: []string{"-size", "5", "-precompute-depth", "13"}, wantErr: true},
		{name: "workers zero", args: []string{"-workers", "0"}, wantErr: true},
		{name: "workers negative", args: []string{"-workers", "-1"}, wantErr: true},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestRunCountMatchesReference(t *testing.T) {
	args := &appArgs{size: 5, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth}

	count := run(context.Background(), monitoring.NewFakeMonitor(), args)

	assert.Equal(t, uint64(1728), count, "Expected 1728 for 5x5 board")
}
