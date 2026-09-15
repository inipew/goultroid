package taskengine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
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
	queue   chan func()
	workers int
	pending atomic.Int64
	active  atomic.Int64
	failed  atomic.Int64

	wg       sync.WaitGroup
	stopCh   chan struct{}
	stopOnce sync.Once
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
	}
}

func (d *durabilityLane) start() {
	if d == nil {
		return
	}
	for i := 0; i < d.workers; i++ {
		d.wg.Add(1)
		go d.loop()
	}
}

func (d *durabilityLane) loop() {
	defer d.wg.Done()
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
	if d == nil || fn == nil {
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

func (d *durabilityLane) drain(ctx context.Context) error {
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

func (d *durabilityLane) stop() {
	if d == nil {
		return
	}
	d.stopOnce.Do(func() { close(d.stopCh) })
	d.wg.Wait()
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
