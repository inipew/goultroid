package taskengine

import (
	"context"
	"sync"
	"sync/atomic"
)

const defaultDurabilityConcurrency = 4

// durabilityLane is a bounded execution/wait lane dedicated to persistence
// acknowledgements. It is deliberately separate from completionDelivery so a
// slow CommitPump or direct commit can never consume user callback workers.
//
// TaskEngine keeps result credits until durability resolves. The lane queue is
// sized to ResultCapacity, so the number of durability-required tasks that can
// reach this lane is itself bounded by the same credit budget.
type durabilityLane struct {
	queue     chan func()
	workers   int
	pending   atomic.Int64
	active    atomic.Int64
	failed    atomic.Int64
	stopping  atomic.Bool
	remaining atomic.Int64

	wg       sync.WaitGroup
	stopCh   chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	doneOnce sync.Once
}

func newDurabilityLane(workers, queueCap int) *durabilityLane {
	if workers <= 0 {
		workers = defaultDurabilityConcurrency
	}
	if queueCap <= 0 {
		queueCap = 1
	}
	return &durabilityLane{
		queue:   make(chan func(), queueCap),
		workers: workers,
		stopCh:  make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (d *durabilityLane) start() {
	if d == nil {
		return
	}
	d.remaining.Store(int64(d.workers))
	for i := 0; i < d.workers; i++ {
		d.wg.Add(1)
		go d.loop()
	}
}

func (d *durabilityLane) workerDone() {
	d.wg.Done()
	if d.remaining.Add(-1) == 0 {
		d.doneOnce.Do(func() { close(d.done) })
	}
}

func (d *durabilityLane) loop() {
	defer d.workerDone()
	for {
		select {
		case <-d.stopCh:
			return
		case fn := <-d.queue:
			d.pending.Add(-1)
			d.active.Add(1)
			func() {
				defer func() {
					_ = recover()
					d.active.Add(-1)
				}()
				fn()
			}()
		}
	}
}

func (d *durabilityLane) enqueue(fn func()) bool {
	if d == nil || fn == nil || d.stopping.Load() {
		if d != nil && fn != nil {
			d.failed.Add(1)
		}
		return false
	}
	d.pending.Add(1)
	select {
	case d.queue <- fn:
		return true
	default:
		d.pending.Add(-1)
		d.failed.Add(1)
		return false
	}
}

// stop prevents new durability work, signals workers, and joins only until ctx
// expires. A Commit implementation is external code and may ignore its context;
// shutdown must still remain bounded when that happens.
func (d *durabilityLane) stop(ctx context.Context) error {
	if d == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.stopping.Store(true)
	d.stopOnce.Do(func() { close(d.stopCh) })
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

func (d *durabilityLane) queueLen() int {
	if d == nil {
		return 0
	}
	return len(d.queue)
}

func (d *durabilityLane) queueCap() int {
	if d == nil {
		return 0
	}
	return cap(d.queue)
}

func (d *durabilityLane) failureCount() int64 {
	if d == nil {
		return 0
	}
	return d.failed.Load()
}
