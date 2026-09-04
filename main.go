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

type appArgs struct {
	size            int
	workers         int
	precomputeDepth int
	oracleDepth     int
}

func parseArgs(args []string) (*appArgs, error) {
	fs := flag.NewFlagSet("knighttour", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	size := fs.Int("size", 5, "Board size (5-8)")
	workers := fs.Int("workers", runtime.NumCPU(), "Number of workers for parallel search")
	precomputeDepth := fs.Int("precompute-depth", counter.DefaultPrecomputeDepth, "Root/subtask generation depth")
	oracleDepth := fs.Int("oracle-depth", 0, "Shape-oracle reversal mask size (0 = legacy prefix-cache reversal)")

	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}

	if *size < 5 || *size > 8 {
		return nil, errors.New("size must be between 5 and 8")
	}

	maxDepth := (*size * *size) / 2
	if *precomputeDepth < 1 || *precomputeDepth > maxDepth {
		return nil, fmt.Errorf("-precompute-depth should be between 1 and %d", maxDepth)
	}

	// The oracle stops at level size*size - oracleDepth; that level must be
	// reachable from the generated roots, otherwise reversal silently never
	// fires while the legacy mode is already off for oracleDepth > 0.
	maxOracleDepth := *size**size - *precomputeDepth
	if *oracleDepth < 0 || *oracleDepth > maxOracleDepth {
		return nil, fmt.Errorf("-oracle-depth should be between 0 and %d for -precompute-depth %d (0 = legacy prefix-cache reversal)", maxOracleDepth, *precomputeDepth)
	}

	if *workers < 1 {
		return nil, errors.New("-workers must be at least 1")
	}

	return &appArgs{size: *size, workers: *workers, precomputeDepth: *precomputeDepth, oracleDepth: *oracleDepth}, nil
}

func run(ctx context.Context, monitor monitoring.Monitor, args *appArgs) uint64 {
	g := graph.New(args.size)
	c := counter.NewCounter(g)
	return c.ParallelCountWithDepth(ctx, monitor, args.workers, args.precomputeDepth, args.oracleDepth)
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
