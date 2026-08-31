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
}

func parseArgs(args []string) (*appArgs, error) {
	fs := flag.NewFlagSet("knighttour", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	size := fs.Int("size", 5, "Board size (5-8)")
	workers := fs.Int("workers", runtime.NumCPU(), "Number of workers for parallel search")
	precomputeDepth := fs.Int("precompute-depth", counter.DefaultPrecomputeDepth, "Precompute depth for subtasks")

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

	if *workers < 1 {
		return nil, errors.New("-workers must be at least 1")
	}

	return &appArgs{size: *size, workers: *workers, precomputeDepth: *precomputeDepth}, nil
}

func run(ctx context.Context, monitor monitoring.Monitor, args *appArgs) uint64 {
	g := graph.New(args.size)
	c := counter.NewCounter(g)
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
