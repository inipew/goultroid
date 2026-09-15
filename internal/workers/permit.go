package workers

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrPermitAlreadyUsed = errors.New("permit already used")
	ErrPermitExpired     = errors.New("permit has expired")
	ErrPermitInvalid     = errors.New("permit is invalid or mismatched")
)

// Permit represents an exclusive reservation of a physical worker slot (ADR 0006 §5.3).
// It carries the pool, worker slot, generation, assigned task ID, and dispatch epoch.
type Permit struct {
	Pool          tasks.PoolID
	WorkerID      int
	Generation    uint64
	TaskID        tasks.TaskID
	DispatchEpoch uint64

	used      atomic.Bool
	onRelease func()
	once      sync.Once
}

// NewPermit constructs a new single-use execution permit.
func NewPermit(pool tasks.PoolID, workerID int, generation uint64, taskID tasks.TaskID, epoch uint64, onRelease func()) *Permit {
	return &Permit{
		Pool:          pool,
		WorkerID:      workerID,
		Generation:    generation,
		TaskID:        taskID,
		DispatchEpoch: epoch,
		onRelease:     onRelease,
	}
}

// Use claims the permit for execution. It returns an error if already used.
func (p *Permit) Use() error {
	if p == nil {
		return ErrPermitInvalid
	}
	if !p.used.CompareAndSwap(false, true) {
		return ErrPermitAlreadyUsed
	}
	return nil
}

// IsUsed reports whether the permit has already been claimed.
func (p *Permit) IsUsed() bool {
	if p == nil {
		return false
	}
	return p.used.Load()
}

// Release returns the reserved slot capacity back to the inventory if not used or upon finish.
func (p *Permit) Release() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		if p.onRelease != nil {
			p.onRelease()
		}
	})
}
