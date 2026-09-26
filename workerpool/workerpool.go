// Package workerpool provides the fixed-crew concurrency mechanics shared by the
// counting pipeline phases: a goroutine crew claiming work indices from one
// atomic cursor behind a Wait barrier (Run) and a producer→consumers fan-out
// over a single buffered channel (Fanout). It knows nothing about the domain —
// no tables, paths or monitors — and carries no error plumbing: the served
// paths return no errors, so context cancellation is the only control flow
// (specs/workerpool.md). Phase policy (batch sizes, channel capacity, clamps)
// stays with the caller.
package workerpool

import (
	"context"
	"iter"
	"sync"
	"sync/atomic"
)

// Pool configures a worker crew: at most workers goroutines per method call.
// The goroutines live inside a single call; the only state between calls is
// the crew size. workers ≥ 1 is the caller contract (main.go validates it).
type Pool struct {
	workers int
}

// New returns a crew of at most workers goroutines per Run call.
func New(workers int) *Pool {
	return &Pool{workers: workers}
}

// Run starts min(workers, max(n,0)) workers and waits for them (barrier). Each
// worker runs once with idx — an iterator over the work indices 0..n-1 claimed
// from one shared atomic cursor (one Add(1) per task); every claim is preceded
// by a ctx check, and iteration ends when the work is exhausted or ctx is
// done. The worker function owns its per-goroutine setup/teardown around the
// index loop (e.g. a batched writer flushed on every exit path). A terminated
// ctx ends claiming early — partial run, no panic; an index already handed out
// is never re-issued. n ≤ 0 starts no goroutines at all.
func (p *Pool) Run(ctx context.Context, n int, worker func(ctx context.Context, idx iter.Seq[int])) {
	var cursor atomic.Int64
	var wg sync.WaitGroup
	for range min(p.workers, max(n, 0)) {
		wg.Go(func() {
			worker(ctx, claims(ctx, &cursor, n))
		})
	}
	wg.Wait()
}

// claims yields indices taken from the shared cursor until the worklist is
// exhausted. ctx is re-checked before every claim: a cancelled run never
// consumes an index it would not process (the cursor value may differ from a
// claim-then-check loop, the set of processed tasks may not).
func claims(ctx context.Context, cursor *atomic.Int64, n int) iter.Seq[int] {
	return func(yield func(int) bool) {
		for ctx.Err() == nil {
			i := int(cursor.Add(1)) - 1
			if i >= n {
				return
			}
			if !yield(i) {
				return
			}
		}
	}
}

// Fanout streams values from produce to consumers goroutines over one channel
// buffered with max(bufSize,1) slots: the consumers drain it until close;
// produce runs on the caller's goroutine right after they start, and when it
// returns the channel is closed and Fanout waits for the drain. Each sent
// element is delivered exactly once in send order (FIFO). Deadlock-free under
// its contract: produce blocks only on sends, consume waits for nothing from
// produce and always drains until close — buffer ≥ 1 keeps that possible
// (bufSize < 1 is treated as 1). consumers ≥ 1 is the caller contract (the
// phase clamp stays with the caller). No derived context is created —
// cancellation semantics live inside produce and consume; buffered elements
// survive cancellation and are still delivered. T is instantiated concretely
// per phase: elements copy by value through the channel, so a produced element
// must not alias a buffer the producer reuses.
func Fanout[T any](ctx context.Context, consumers, bufSize int,
	produce func(ctx context.Context, out chan<- T),
	consume func(ctx context.Context, batch T),
) {
	ch := make(chan T, max(bufSize, 1))

	var wg sync.WaitGroup
	wg.Add(consumers)
	for range consumers {
		go func() {
			defer wg.Done()
			for batch := range ch {
				consume(ctx, batch)
			}
		}()
	}

	produce(ctx, ch)
	close(ch) // the producer contour returned — every value was sent.
	wg.Wait()
}
