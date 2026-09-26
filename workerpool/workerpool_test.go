package workerpool

import (
	"context"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The claim cursor hands every index of 0..n-1 to exactly one worker under
// concurrent claiming (specs/workerpool.md): double claims would process a
// task twice, a lost index would drop it.
func TestRunClaimsEachIndexOnce(t *testing.T) {
	tests := []struct {
		name    string
		workers int
		n       int
	}{
		{name: "single worker", workers: 1, n: 7},
		{name: "fewer tasks than workers", workers: 8, n: 3},
		{name: "many more tasks than workers", workers: 8, n: 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := make([]atomic.Bool, tt.n)
			var processed atomic.Int64
			New(tt.workers).Run(context.Background(), tt.n, func(_ context.Context, idx iter.Seq[int]) {
				for i := range idx {
					assert.False(t, seen[i].Swap(true), "index %d claimed twice", i)
					processed.Add(1)
				}
			})
			assert.Equal(t, int64(tt.n), processed.Load(), "every index processed exactly once")
		})
	}
}

// The crew is clamped to min(workers, max(n,0)): the worker function runs
// exactly once per started goroutine — no more goroutines than tasks, none
// for a non-positive worklist.
func TestRunStartsClampedCrew(t *testing.T) {
	tests := []struct {
		name     string
		workers  int
		n        int
		wantCrew int64
	}{
		{name: "crew shrinks to the worklist", workers: 8, n: 3, wantCrew: 3},
		{name: "crew capped by its size", workers: 3, n: 50, wantCrew: 3},
		{name: "empty worklist", workers: 4, n: 0, wantCrew: 0},
		{name: "negative worklist", workers: 4, n: -5, wantCrew: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var started atomic.Int64
			New(tt.workers).Run(context.Background(), tt.n, func(_ context.Context, idx iter.Seq[int]) {
				started.Add(1)
				for range idx {
				}
			})
			assert.Equal(t, tt.wantCrew, started.Load(), "started workers")
		})
	}
}

// A terminated context — cancelled or expired alike — ends Run as a partial
// run without a panic: claims stop before consuming any index (zero processed
// when already terminated at entry).
func TestRunTerminatedContextProcessesNothing(t *testing.T) {
	tests := []struct {
		ctxFunc func() context.Context
		name    string
	}{
		{name: "cancelled", ctxFunc: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
		{name: "deadline exceeded", ctxFunc: func() context.Context {
			ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
			defer cancel()
			return ctx
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var processed atomic.Int64
			assert.NotPanics(t, func() {
				New(4).Run(tt.ctxFunc(), 100, func(_ context.Context, idx iter.Seq[int]) {
					for range idx {
						processed.Add(1)
					}
				})
			}, "a terminated context must end Run without a panic")
			assert.Zero(t, processed.Load(), "no index is claimed after termination")
		})
	}
}

// Cancellation mid-run stops the claim loop at the next check: with a single
// worker cancelling inside its first task, exactly that one index is ever
// processed — no reprocessing, no further claims.
func TestRunCancellationStopsClaims(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var processed []int
	New(1).Run(ctx, 100, func(_ context.Context, idx iter.Seq[int]) {
		for i := range idx {
			mu.Lock()
			processed = append(processed, i)
			mu.Unlock()
			cancel()
		}
	})

	assert.Equal(t, []int{0}, processed, "the loop stops after the cancelling task")
}

// Run is a barrier: every worker's side effect is visible after it returns,
// and consecutive calls on one Pool start from a fresh cursor (no state leaks
// between phases).
func TestRunBarrierAndCursorReset(t *testing.T) {
	var sum atomic.Int64
	pool := New(4)

	for range 2 {
		sum.Store(0)
		pool.Run(context.Background(), 10, func(_ context.Context, idx iter.Seq[int]) {
			for i := range idx {
				time.Sleep(time.Millisecond) // keep the goroutine alive past the claim
				sum.Add(int64(i))
			}
		})
		assert.Equal(t, int64(45), sum.Load(), "after the barrier every task has been counted")
	}
}

// Breaking out of the index loop early is legal: Run returns and the worker
// claims nothing more (the rest of the worklist simply goes unclaimed).
func TestRunEarlyBreakReturns(t *testing.T) {
	var processed atomic.Int64
	New(2).Run(context.Background(), 100, func(_ context.Context, idx iter.Seq[int]) {
		for range idx {
			processed.Add(1)
			break
		}
	})
	assert.Equal(t, int64(2), processed.Load(), "each worker claimed its first index and stopped")
}

// collector gathers Fanout deliveries across consumer goroutines.
type collector struct {
	items []int
	mu    sync.Mutex
}

// add records one delivered value under the collector's lock.
func (c *collector) add(v int) {
	c.mu.Lock()
	c.items = append(c.items, v)
	c.mu.Unlock()
}

// Fanout delivers every produced value exactly once in FIFO order whatever the
// buffer and crew sizes — a one-slot buffer forces every send to wait for a
// drain (whole-slot backpressure) without deadlocking (specs/workerpool.md).
func TestFanoutDeliversExactOnce(t *testing.T) {
	tests := []struct {
		name      string
		consumers int
		bufSize   int
		items     int
	}{
		{name: "one slot forces backpressure", consumers: 4, bufSize: 1, items: 1000},
		{name: "two slots", consumers: 4, bufSize: 2, items: 1000},
		{name: "everything fits the buffer", consumers: 1, bufSize: 64, items: 50},
		{name: "lone consumer, lone item", consumers: 1, bufSize: 1, items: 1},
		{name: "empty production", consumers: 3, bufSize: 2, items: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got collector
			Fanout(context.Background(), tt.consumers, tt.bufSize,
				func(_ context.Context, out chan<- int) {
					for i := range tt.items {
						out <- i
					}
				},
				func(_ context.Context, batch int) { got.add(batch) })

			want := make([]int, tt.items)
			for i := range want {
				want[i] = i
			}
			// Multiset equality: a duplicate delivery fails just like a loss;
			// cross-consumer arrival order is not the contract.
			assert.ElementsMatch(t, want, got.items, "every value delivered exactly once")
		})
	}
}

// A terminated context stops production at the next check, but everything
// already sent — including what sits in the buffer at close — is still
// delivered: consumed equals produced, and Fanout returns. The decision to
// ignore drained values under cancellation belongs to consume, not the pool.
func TestFanoutCancelInProduceDrainsSent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const stopAfter = 10
	var sent atomic.Int64
	var got collector
	Fanout(ctx, 2, 4,
		func(ctx context.Context, out chan<- int) {
			for i := 0; ; i++ {
				if ctx.Err() != nil {
					return
				}
				select {
				case out <- i:
					sent.Add(1)
				case <-ctx.Done():
					return
				}
			}
		},
		func(_ context.Context, batch int) {
			got.add(batch)
			// >= : with concurrent consumers an exact-equality check can be
			// skipped (another add lands between this add and the count).
			if got.count() >= stopAfter {
				cancel()
			}
		})

	assert.GreaterOrEqual(t, got.count(), stopAfter, "consumption reached the cancel point")
	assert.Equal(t, int(sent.Load()), got.count(), "everything sent before close was drained")
}

// count snapshots the collected length under the collector's lock.
func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
