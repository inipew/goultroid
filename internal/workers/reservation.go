package workers

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

const PoolInteractive = "interactive"

var ErrNoExecutionCapacity = errors.New("no physical execution capacity available")

// ExecutionReservation reserves one physical execution slot before a durable
// producer claims work. Releasing is idempotent so callers can safely cover
// cancellation, submission failure, and the queued->running handoff.
type ExecutionReservation struct {
	counter *atomic.Int32
	once    sync.Once
}

func (r *ExecutionReservation) Release() {
	if r == nil || r.counter == nil {
		return
	}
	r.once.Do(func() { r.counter.Add(-1) })
}

var poolReservations sync.Map // map[*Pool]*atomic.Int32

func reservationCounter(pool *Pool) *atomic.Int32 {
	counter, _ := poolReservations.LoadOrStore(pool, &atomic.Int32{})
	return counter.(*atomic.Int32)
}

// TryReserveExecution atomically reserves a physical worker slot for poolName.
// It is intentionally non-blocking: durable producers must not claim leases
// unless execution capacity is available now.
func (m *Manager) TryReserveExecution(poolName string) (*ExecutionReservation, error) {
	m.mu.RLock()
	pool, ok := m.pools[poolName]
	accepting := m.accepting
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("worker pool %q not found", poolName)
	}
	if !accepting || !pool.running.Load() {
		return nil, ErrNoExecutionCapacity
	}

	counter := reservationCounter(pool)
	for {
		reserved := counter.Load()
		busy := pool.busy.Load()
		if int(busy+reserved) >= pool.concurrency {
			return nil, ErrNoExecutionCapacity
		}
		if counter.CompareAndSwap(reserved, reserved+1) {
			return &ExecutionReservation{counter: counter}, nil
		}
	}
}
