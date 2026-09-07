package client

import (
	"sync/atomic"
)

// ClientState represents the operational phase of the assistant client.
type ClientState uint32

const (
	StateNew ClientState = iota
	StateStarting
	StateRunning
	StateStopping
	StateStopped
	StateFailed
)

// String returns a human-readable representation of ClientState.
func (s ClientState) String() string {
	switch s {
	case StateNew:
		return "new"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Lifecycle tracks the client state using atomic operations.
type Lifecycle struct {
	state uint32
}

// NewLifecycle creates a Lifecycle initialized to StateNew.
func NewLifecycle() *Lifecycle {
	return &Lifecycle{
		state: uint32(StateNew),
	}
}

// State returns the current client state.
func (l *Lifecycle) State() ClientState {
	return ClientState(atomic.LoadUint32(&l.state))
}

// SetState updates the current client state.
func (l *Lifecycle) SetState(s ClientState) {
	atomic.StoreUint32(&l.state, uint32(s))
}

// TryStart attempts to transition to StateStarting if the client is New or Stopped.
func (l *Lifecycle) TryStart() bool {
	for {
		cur := l.State()
		if cur == StateStarting || cur == StateRunning {
			return false
		}
		if atomic.CompareAndSwapUint32(&l.state, uint32(cur), uint32(StateStarting)) {
			return true
		}
	}
}

// TryStop attempts to transition to StateStopping if the client is Running or Starting.
func (l *Lifecycle) TryStop() bool {
	for {
		cur := l.State()
		if cur == StateStopping || cur == StateStopped {
			return false
		}
		if atomic.CompareAndSwapUint32(&l.state, uint32(cur), uint32(StateStopping)) {
			return true
		}
	}
}
