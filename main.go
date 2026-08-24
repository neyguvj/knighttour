package main

import (
	"context"
	"flag"
	"log"

	"knighttour/counter"
	"knighttour/graph"
	"knighttour/monitoring"
)

func main() {
	size := flag.Int("size", 5, "Board size (5-8)")
	workers := flag.Int("workers", 1, "Number of workers for parallel search")
	precomputeDepth := flag.Int("precompute-depth", counter.DefaultPrecomputeDepth, "Precompute depth for subtasks")

	flag.Parse()

	if *size < 5 || *size > 8 {
		log.Fatal("Size must be between 5 and 8")
	}

	if *precomputeDepth < 1 || *precomputeDepth > ((*size)*(*size))/2 {
		log.Fatalf("-precompute-depth should be between 1 and %d", *size**size/2)
	}

	if *workers < 1 {
		log.Fatal("-workers must be at least 1")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	realMonitor := monitoring.NewMonitor()
	realMonitor.Start(ctx)
	defer realMonitor.Finish()

	g := graph.New(*size)
	c := counter.NewCounter(g)

	c.ParallelCountWithDepth(ctx, realMonitor, *workers, *precomputeDepth)
}
