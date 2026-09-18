package taskengine

import (
	"errors"
)

// SetDurabilityConcurrency configures the fixed durability-commit worker lane.
// It must be called before Start because replacing a live lane would abandon
// queued commit acknowledgements.
func (e *Engine) SetDurabilityConcurrency(workers int) error {
	if e == nil {
		return errors.New("taskengine: engine is nil")
	}
	if workers <= 0 {
		return errors.New("taskengine: durability concurrency must be positive")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runStarted || e.rootCtx != nil {
		return errors.New("taskengine: durability concurrency cannot change after start")
	}
	e.durability = newDurabilityLane(workers, e.config.ResultCapacity)
	return nil
}
