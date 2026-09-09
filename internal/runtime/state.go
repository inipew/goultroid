package runtime

import (
	"fmt"
	"sync"
)

// State represents the observable lifecycle state of the Runtime.
type State string

const (
	StateCreated      State = "created"
	StateInitializing State = "initializing"
	StateStarting     State = "starting"
	StateRunning      State = "running"
	StateStopping     State = "stopping"
	StateStopped      State = "stopped"
	StateFailed       State = "failed"
)

// String returns the string representation of the state.
func (s State) String() string {
	return string(s)
}

// IsTerminal returns true if the state is final and cannot transition further.
func (s State) IsTerminal() bool {
	return s == StateStopped || s == StateFailed
}

// IsRunning returns true if the runtime is currently running.
func (s State) IsRunning() bool {
	return s == StateRunning
}

// StateMachine manages thread-safe state transitions for the Runtime.
type StateMachine struct {
	mu    sync.RWMutex
	state State
}

// NewStateMachine creates a new StateMachine initialized to StateCreated.
func NewStateMachine() *StateMachine {
	return &StateMachine{state: StateCreated}
}

// Current returns the current state.
func (sm *StateMachine) Current() State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state
}

// Transition attempts to transition from current state to next state according to valid rules.
func (sm *StateMachine) Transition(next State) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.isValidTransition(sm.state, next) {
		sm.state = next
		return nil
	}

	return fmt.Errorf("invalid runtime state transition from %s to %s", sm.state, next)
}

// SetFailed transitions the state to StateFailed from any non-terminal state.
func (sm *StateMachine) SetFailed() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state = StateFailed
}

func (sm *StateMachine) isValidTransition(current, next State) bool {
	switch current {
	case StateCreated:
		return next == StateInitializing || next == StateFailed
	case StateInitializing:
		return next == StateStarting || next == StateFailed || next == StateStopping
	case StateStarting:
		return next == StateRunning || next == StateFailed || next == StateStopping
	case StateRunning:
		return next == StateStopping || next == StateFailed
	case StateStopping:
		return next == StateStopped || next == StateFailed
	case StateStopped, StateFailed:
		return false
	default:
		return false
	}
}
