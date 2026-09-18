package tasks

import (
	"context"
)

// Ticket represents an admitted task and permits waiting for its final result (ADR 0006 §5.1).
type Ticket interface {
	TaskID() TaskID
	State() TaskState
	Done() <-chan struct{}
	Result() (TaskResult, bool)
	Wait(ctx context.Context) (TaskResult, error)
}

// CancelReceipt reports whether cancellation was accepted and in which state.
type CancelReceipt struct {
	TaskID   TaskID    `json:"task_id"`
	Accepted bool      `json:"accepted"`
	State    TaskState `json:"state"`
	Reason   Cause     `json:"reason"`
}

// Client is the consumer-facing interface to submit work, cancel active tasks, and inspect status.
type Client interface {
	Submit(ctx context.Context, spec WorkSpec) (Ticket, error)
	Cancel(id TaskID, reason Cause) (CancelReceipt, error)
	CancelScope(scope ScopeIdentity, reason Cause) int
	Snapshot(id TaskID) (TaskSnapshot, bool)
}
