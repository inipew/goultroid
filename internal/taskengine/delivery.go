package taskengine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// Bounded completion-callback delivery (Phase B5).
//
// The old engine spawned one unbounded goroutine per terminal task
// (`go fn(res)`). Under burst load that is unbounded goroutine/memory growth,
// and a blocking callback was invisible to Drain. This delivery service uses a
// fixed worker pool over a bounded queue:
//
//   - enqueue is non-blocking for the single-writer runLoop; the queue is
//     sized to at least ResultCapacity so every admitted task always has a
//     delivery slot without blocking the control loop. Only a pathological
//     divergence (workers permanently stuck AND queue full) falls back to a
//     detached goroutine, counted in fallbacks for observability.
//   - drain lets Stop/Drain wait for in-flight callbacks under a context
//     deadline instead of returning while user callbacks still run.
//   - every callback runs under panic isolation so one bad consumer cannot
//     kill a delivery worker.

type deliveryItem struct {
	fn  func(tasks.TaskResult)
	res tasks.TaskResult
}

type completionDelivery struct {
	queue    chan deliveryItem
	workers  int
	pending  atomic.Int64
	active   atomic.Int64
	fallback atomic.Int64

	wg     sync.WaitGroup
	stopCh chan struct{}
	stopOs sync.Once
}

func newCompletionDelivery(workers, queueCap int) *completionDelivery {
	if workers <= 0 {
		workers = DefaultDeliveryConcurrency
	}
	if queueCap <= 0 {
		queueCap = 256
	}
	return &completionDelivery{
		queue:   make(chan deliveryItem, queueCap),
		workers: workers,
		stopCh:  make(chan struct{}),
	}
}

func (d *completionDelivery) start() {
	for i := 0; i < d.workers; i++ {
		d.wg.Add(1)
		go d.loop()
	}
}

func (d *completionDelivery) loop() {
	defer d.wg.Done()
	for {
		select {
		case <-d.stopCh:
			return
		case item := <-d.queue:
			d.pending.Add(-1)
			d.active.Add(1)
			func() {
				defer func() { _ = recover() }()
				defer d.active.Add(-1)
				item.fn(item.res)
			}()
		}
	}
}

// enqueue hands a callback to the delivery pool without blocking the caller.
// The bounded queue always has room in steady state (sized >= result
// capacity); on overflow it degrades to a detached isolated goroutine and
// counts the event.
func (d *completionDelivery) enqueue(fn func(tasks.TaskResult), res tasks.TaskResult) {
	if d == nil || fn == nil {
		return
	}
	d.pending.Add(1)
	select {
	case d.queue <- deliveryItem{fn: fn, res: res}:
	default:
		d.fallback.Add(1)
		go func() {
			defer func() { _ = recover() }()
			defer d.pending.Add(-1)
			fn(res)
		}()
	}
}

// drain waits until all enqueued callbacks have been dequeued and all active
// callbacks have returned, or ctx expires.
func (d *completionDelivery) drain(ctx context.Context) error {
	if d == nil {
		return nil
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if d.pending.Load() == 0 && d.active.Load() == 0 && len(d.queue) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// stop signals workers to exit after Stop has drained the queue.
func (d *completionDelivery) stop() {
	if d == nil {
		return
	}
	d.stopOs.Do(func() { close(d.stopCh) })
	d.wg.Wait()
}

func (d *completionDelivery) queueLen() int {
	if d == nil {
		return 0
	}
	return len(d.queue)
}

func (d *completionDelivery) queueCap() int {
	if d == nil {
		return 0
	}
	return cap(d.queue)
}

func (d *completionDelivery) fallbackCount() int64 {
	if d == nil {
		return 0
	}
	return d.fallback.Load()
}
