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
// Delivery capacity is reserved at task admission for every WorkSpec with an
// OnComplete callback. A reservation is held until that callback returns. This
// turns a blocked callback consumer into admission backpressure and guarantees
// that terminal settlement never needs an unbounded detached-goroutine escape
// hatch. Queue overflow after a valid reservation is therefore an invariant
// violation: the callback is failed closed and counted, never spawned.
type deliveryItem struct {
	fn      func(tasks.TaskResult)
	res     tasks.TaskResult
	release bool
}

type completionDelivery struct {
	queue        chan deliveryItem
	reservations chan struct{}
	workers      int
	pending      atomic.Int64
	active       atomic.Int64
	failed       atomic.Int64

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
		queue:        make(chan deliveryItem, queueCap),
		reservations: make(chan struct{}, queueCap),
		workers:      workers,
		stopCh:       make(chan struct{}),
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
				defer func() {
					_ = recover()
					d.active.Add(-1)
					if item.release {
						d.releaseReservation()
					}
				}()
				item.fn(item.res)
			}()
		}
	}
}

// reserve claims one callback-delivery credit. It is intentionally
// non-blocking because admission must remain bounded and explicit.
func (d *completionDelivery) reserve() bool {
	if d == nil {
		return false
	}
	select {
	case d.reservations <- struct{}{}:
		return true
	default:
		return false
	}
}

func (d *completionDelivery) releaseReservation() {
	if d == nil {
		return
	}
	select {
	case <-d.reservations:
	default:
		// Defensive only: a missing reservation is an internal invariant bug.
		d.failed.Add(1)
	}
}

// enqueueReserved hands off a callback whose delivery credit was reserved at
// admission. Since reservations are bounded by queue capacity and each
// reservation can have at most one queued callback, the non-blocking send must
// succeed. If it does not, fail closed: release the credit and count the
// invariant violation instead of creating a goroutine.
func (d *completionDelivery) enqueueReserved(fn func(tasks.TaskResult), res tasks.TaskResult) bool {
	if d == nil || fn == nil {
		if d != nil {
			d.releaseReservation()
		}
		return false
	}
	d.pending.Add(1)
	select {
	case d.queue <- deliveryItem{fn: fn, res: res, release: true}:
		return true
	default:
		d.pending.Add(-1)
		d.failed.Add(1)
		d.releaseReservation()
		return false
	}
}

// enqueue is retained for internal compatibility. New TaskEngine completion
// paths reserve at admission and call enqueueReserved. Callers without a prior
// reservation receive the same bounded failure policy.
func (d *completionDelivery) enqueue(fn func(tasks.TaskResult), res tasks.TaskResult) bool {
	if d == nil || fn == nil || !d.reserve() {
		if d != nil && fn != nil {
			d.failed.Add(1)
		}
		return false
	}
	return d.enqueueReserved(fn, res)
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

func (d *completionDelivery) reservationLen() int {
	if d == nil {
		return 0
	}
	return len(d.reservations)
}

// fallbackCount keeps the existing diagnostics field/API name while its
// semantics are now bounded delivery failures; detached fallbacks no longer
// exist.
func (d *completionDelivery) fallbackCount() int64 {
	if d == nil {
		return 0
	}
	return d.failed.Load()
}
