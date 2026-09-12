package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"syscall"

	"knighttour/counter"
	"knighttour/graph"
	"knighttour/monitoring"
)

// CLI mode names accepted by -mode (specs/main.md).
const (
	modeClass    = "class"
	modeReversal = "reversal"
)

// mode leads: govet/fieldalignment (make fix) requires pointer-bearing fields
// first — the layout mirrors specs/main.md.
type appArgs struct {
	mode            string
	size            int
	workers         int
	precomputeDepth int
	tailMemo        int
}

func parseArgs(args []string) (*appArgs, error) {
	fs := flag.NewFlagSet("knighttour", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	size := fs.Int("size", 5, "Board size (5-8)")
	workers := fs.Int("workers", runtime.NumCPU(), "Number of workers for parallel search")
	precomputeDepth := fs.Int("precompute-depth", 0, "Root/subtask generation depth (default: per board size)")
	tailMemo := fs.Int("tail-memo", 0, "Counting tail memo: persist f(cur,todo) with popcount(todo) ≤ N between shapes of one worker (0 = off)")
	mode := fs.String("mode", modeClass, "Counting mode: class | reversal")

	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}

	if *size < 5 || *size > 8 {
		return nil, errors.New("size must be between 5 and 8")
	}

	depth := *precomputeDepth
	if depth == 0 && !isFlagSet(fs, "precompute-depth") {
		depth = counter.DefaultPrecomputeDepth(*size)
	}

	maxDepth := *size * *size / 2 // meet-in-the-middle: deeper cuts are the reversed tour's dual
	if depth < 1 || depth > maxDepth {
		return nil, fmt.Errorf("-precompute-depth should be between 1 and %d", maxDepth)
	}

	if *workers < 1 {
		return nil, errors.New("-workers must be at least 1")
	}

	if *tailMemo < 0 {
		return nil, errors.New("-tail-memo must be non-negative (0 = off)")
	}

	if !slices.Contains([]string{modeClass, modeReversal}, *mode) {
		return nil, fmt.Errorf("-mode must be one of %q or %q, got %q", modeClass, modeReversal, *mode)
	}

	return &appArgs{size: *size, workers: *workers, precomputeDepth: depth, tailMemo: *tailMemo, mode: *mode}, nil
}

// isFlagSet reports whether the flag was explicitly provided on the command line.
func isFlagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// counterMode maps a validated -mode name to the counter pipeline; unknown
// names are rejected by parseArgs, so the fallback is unreachable in practice.
func counterMode(mode string) counter.Mode {
	if mode == modeReversal {
		return counter.ModeReversal
	}
	return counter.ModeClass
}

func run(ctx context.Context, monitor monitoring.Monitor, args *appArgs) uint64 {
	g := graph.New(args.size)
	c := counter.NewCounter(g)
	c.SetTailMemo(args.tailMemo, 0)
	c.SetMode(counterMode(args.mode))
	return c.ParallelCountWithDepth(ctx, monitor, args.workers, args.precomputeDepth)
}

func main() {
	args, err := parseArgs(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	realMonitor := monitoring.NewMonitor()
	realMonitor.Start(ctx)
	defer realMonitor.Finish()

	run(ctx, realMonitor, args)

	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		fmt.Println("\nInterrupted: showing partial results")
	}
}
