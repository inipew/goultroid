package taskengine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

const defaultLaneIdleTimeout = 30 * time.Second

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
	idleTimeout  time.Duration
	pending      atomic.Int64
	active       atomic.Int64
	failed       atomic.Int64
	stopping     atomic.Bool
	remaining    atomic.Int64

	workerMu sync.Mutex
	stopCh   chan struct{}
	stopOs   sync.Once
	done     chan struct{}
	doneOnce sync.Once
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
		idleTimeout:  defaultLaneIdleTimeout,
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// start is intentionally passive. Workers are created only after callback work
// is queued, so an idle TaskEngine does not retain completion goroutines.
func (d *completionDelivery) start() {
	if d == nil {
		return
	}
	if d.idleTimeout <= 0 {
		d.idleTimeout = defaultLaneIdleTimeout
	}
}

func (d *completionDelivery) ensureWorkers() {
	if d == nil || d.stopping.Load() {
		return
	}

	d.workerMu.Lock()
	defer d.workerMu.Unlock()
	if d.stopping.Load() {
		return
	}

	target := len(d.queue)
	if target < 1 {
		target = 1
	}
	if target > d.workers {
		target = d.workers
	}
	running := int(d.remaining.Load())
	for running < target {
		d.remaining.Add(1)
		running++
		go d.loop()
	}
}

func (d *completionDelivery) workerDone() {
	remaining := d.remaining.Add(-1)
	if d.stopping.Load() {
		if remaining == 0 {
			d.doneOnce.Do(func() { close(d.done) })
		}
		return
	}
	if len(d.queue) > 0 {
		d.ensureWorkers()
	}
}

func (d *completionDelivery) loop() {
	defer d.workerDone()
	timer := time.NewTimer(d.idleTimeout)
	defer timer.Stop()

	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d.idleTimeout)
	}

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
			resetTimer()
		case <-timer.C:
			if len(d.queue) == 0 {
				return
			}
			timer.Reset(d.idleTimeout)
		}
	}
}

// reserve claims one callback-delivery credit. It is intentionally
// non-blocking because admission must remain bounded and explicit.
func (d *completionDelivery) reserve() bool {
	if d == nil || d.stopping.Load() {
		return false
	}
	select {
	case d.reservations <- struct{}{}:
		if d.stopping.Load() {
			d.releaseReservation()
			return false
		}
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
// succeed. If shutdown has started or the invariant is violated, fail closed:
// release the credit and count the failure instead of creating a goroutine.
func (d *completionDelivery) enqueueReserved(fn func(tasks.TaskResult), res tasks.TaskResult) bool {
	if d == nil || fn == nil {
		if d != nil {
			d.releaseReservation()
		}
		return false
	}
	if d.stopping.Load() {
		d.failed.Add(1)
		d.releaseReservation()
		return false
	}
	d.pending.Add(1)
	select {
	case d.queue <- deliveryItem{fn: fn, res: res, release: true}:
		d.ensureWorkers()
		return true
	default:
		d.pending.Add(-1)
		d.failed.Add(1)
		d.releaseReservation()
		return false
	}
}

// drain waits until all enqueued callbacks have been dequeued and all active
// callbacks have returned, or ctx expires.
func (d *completionDelivery) drain(ctx context.Context) error {
	if d == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
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

// stop prevents new delivery reservations, signals workers, and waits only as
// long as ctx allows. A user callback is arbitrary code and cannot be killed in
// Go; a wedged callback therefore must not turn Engine.Stop into an unbounded
// join. The worker exits naturally if/when that callback returns.
func (d *completionDelivery) stop(ctx context.Context) error {
	if d == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.stopping.Store(true)
	d.stopOs.Do(func() { close(d.stopCh) })
	if d.remaining.Load() == 0 {
		d.doneOnce.Do(func() { close(d.done) })
	}
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// fallbackCount keeps the existing diagnostics field/API name while its
// semantics are now bounded delivery failures; detached fallbacks no longer
// exist.
func (d *completionDelivery) fallbackCount() int64 {
	if d == nil {
		return 0
	}
	return d.failed.Load()
}
