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
			expected: &appArgs{size: 5, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(5), mode: modeReversal, gcPercent: counter.DefaultGCPercentReversal},
		},
		{
			name:     "explicit flags",
			args:     []string{"-size", "6", "-workers", "4", "-precompute-depth", "3"},
			expected: &appArgs{size: 6, workers: 4, precomputeDepth: 3, mode: modeReversal, gcPercent: counter.DefaultGCPercentReversal},
		},
		{name: "board 5 default depth", args: []string{"-size", "5"}, expected: &appArgs{size: 5, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(5), mode: modeReversal, gcPercent: counter.DefaultGCPercentReversal}},
		{name: "board 6 default depth", args: []string{"-size", "6"}, expected: &appArgs{size: 6, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(6), mode: modeReversal, gcPercent: counter.DefaultGCPercentReversal}},
		{name: "board 7 default depth", args: []string{"-size", "7"}, expected: &appArgs{size: 7, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(7), mode: modeReversal, gcPercent: counter.DefaultGCPercentReversal}},
		{name: "board 8 default depth", args: []string{"-size", "8"}, expected: &appArgs{size: 8, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(8), mode: modeReversal, gcPercent: counter.DefaultGCPercentReversal}},
		{name: "board 8 max depth", args: []string{"-size", "8", "-precompute-depth", "32"}, expected: &appArgs{size: 8, workers: runtime.NumCPU(), precomputeDepth: 32, mode: modeReversal, gcPercent: counter.DefaultGCPercentReversal}},
		{name: "mode class explicit", args: []string{"-mode", "class"}, expected: &appArgs{size: 5, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(5), mode: modeClass, gcPercent: counter.DefaultGCPercentReversal}},
		{name: "mode reversal", args: []string{"-size", "6", "-mode", "reversal"}, expected: &appArgs{size: 6, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(6), mode: modeReversal, gcPercent: counter.DefaultGCPercentReversal}},
		{name: "gc percent off", args: []string{"-gc-percent", "0"}, expected: &appArgs{size: 5, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(5), mode: modeReversal, gcPercent: 0}},
		{name: "gc percent explicit", args: []string{"-mode", "reversal", "-gc-percent", "60"}, expected: &appArgs{size: 5, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(5), mode: modeReversal, gcPercent: 60}},
		{name: "gc percent negative", args: []string{"-gc-percent", "-1"}, wantErr: true},
		{name: "unknown mode", args: []string{"-mode", "oracle"}, wantErr: true},
		{name: "empty mode", args: []string{"-mode", ""}, wantErr: true},
		{name: "size too small", args: []string{"-size", "4"}, wantErr: true},
		{name: "size too large", args: []string{"-size", "9"}, wantErr: true},
		{name: "depth zero explicit", args: []string{"-size", "5", "-precompute-depth", "0"}, wantErr: true},
		{name: "depth above half board", args: []string{"-size", "5", "-precompute-depth", "13"}, wantErr: true},
		{
			name:     "tail memo explicit",
			args:     []string{"-size", "6", "-tail-memo", "12"},
			expected: &appArgs{size: 6, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(6), tailMemo: 12, mode: modeReversal, gcPercent: counter.DefaultGCPercentReversal},
		},
		{name: "tail memo negative", args: []string{"-tail-memo", "-1"}, wantErr: true},
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
	tests := []struct {
		name string
		mode string
	}{
		{name: "class", mode: modeClass},
		{name: "reversal", mode: modeReversal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := &appArgs{size: 5, workers: runtime.NumCPU(), precomputeDepth: counter.DefaultPrecomputeDepth(5), mode: tt.mode}

			count := run(context.Background(), monitoring.NewFakeMonitor(), args)

			assert.Equal(t, uint64(1728), count, "Expected 1728 for 5x5 board, mode %s", tt.mode)
		})
	}
}
