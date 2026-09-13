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
	"syscall"

	"knighttour/counter"
	"knighttour/graph"
	"knighttour/monitoring"
)

// appArgs mirrors the validated CLI surface of specs/main.md.
type appArgs struct {
	size            int
	workers         int
	precomputeDepth int
	gcPercent       int
}

func parseArgs(args []string) (*appArgs, error) {
	fs := flag.NewFlagSet("knighttour", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	size := fs.Int("size", 5, "Board size (5-8)")
	workers := fs.Int("workers", runtime.NumCPU(), "Number of workers for parallel search")
	precomputeDepth := fs.Int("precompute-depth", 0, "Root/subtask generation depth (default: per board size)")
	gcPercent := fs.Int("gc-percent", counter.DefaultGCPercentReversal, "GOGC applied for the duration of the counting pipeline (0 = leave the runtime GC untouched)")

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

	if *gcPercent < 0 {
		return nil, errors.New("-gc-percent must be non-negative (0 = leave the runtime GC untouched)")
	}

	return &appArgs{size: *size, workers: *workers, precomputeDepth: depth, gcPercent: *gcPercent}, nil
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

func run(ctx context.Context, monitor monitoring.Monitor, args *appArgs) uint64 {
	g := graph.New(args.size)
	c := counter.NewCounter(g)
	c.SetGCPercent(args.gcPercent) // GOGC for the pipeline duration (ADR-014)
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
