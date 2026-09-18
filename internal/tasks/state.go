package tasks

// TaskState is the lifecycle state managed exclusively by TaskEngine.
type TaskState string

const (
	StateCreated     TaskState = "created"
	StateAdmitted    TaskState = "admitted"
	StateQueued      TaskState = "queued"
	StateDispatching TaskState = "dispatching"
	StateRunning     TaskState = "running"
	// StateCommitPending is the post-execution durability phase: the worker
	// has finished but the result is not yet durably acknowledged. It is the
	// public projection of DurabilityState CommitPending (Phase C).
	StateCommitPending TaskState = "commit_pending"
	StateCompleted     TaskState = "completed"
	StateFailed        TaskState = "failed"
	StateCancelled     TaskState = "cancelled"
	StateTimedOut      TaskState = "timed_out"
	// StateRecoveryRequired is terminal-but-uncertain: physical execution
	// finished but durable acknowledgement failed or was abandoned (shutdown).
	// The durable store is the source of truth for recovery (Phase D).
	StateRecoveryRequired TaskState = "recovery_required"
)
