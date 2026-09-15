package taskengine

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/inipew/goultroid/internal/tasks"
)

var (
	errPermitAlreadyUsed = errors.New("execution permit already used")
	errPermitExpired     = errors.New("execution permit expired")
	errPermitInvalid     = errors.New("execution permit does not match assignment")
)

// permit is TaskEngine-owned physical-slot state. It is never exposed to a
// producer, which prevents a caller from forging or transferring a grant.
type permit struct {
	pool          tasks.PoolID
	workerID      int
	generation    uint64
	taskID        tasks.TaskID
	dispatchEpoch uint64

	state     atomic.Uint32 // 0 reserved, 1 started, 2 released
	onRelease func()
	once      sync.Once
}

func newPermit(pool tasks.PoolID, workerID int, generation uint64, taskID tasks.TaskID, epoch uint64, onRelease func()) *permit {
	return &permit{pool: pool, workerID: workerID, generation: generation, taskID: taskID, dispatchEpoch: epoch, onRelease: onRelease}
}

func (p *permit) use(spec tasks.WorkSpec) error {
	if p == nil || p.pool != spec.Pool || p.taskID != spec.ID {
		return errPermitInvalid
	}
	if !p.state.CompareAndSwap(0, 1) {
		if p.state.Load() == 2 {
			return errPermitExpired
		}
		return errPermitAlreadyUsed
	}
	return nil
}

func (p *permit) release() {
	if p == nil {
		return
	}
	p.state.Store(2)
	p.once.Do(func() {
		if p.onRelease != nil {
			p.onRelease()
		}
	})
}
