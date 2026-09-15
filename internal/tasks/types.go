package tasks

import "time"

// Typed identifiers for the execution runtime (ADR 0006).
type TaskID string
type OwnerID string
type PoolID string
type AttemptID string
type OccurrenceID string

// PriorityClass represents hierarchical deficit round-robin scheduling priorities.
type PriorityClass string

const (
	PriorityInteractive PriorityClass = "interactive"
	PriorityNormal      PriorityClass = "normal"
	PriorityBackground  PriorityClass = "background"
	PriorityMaintenance PriorityClass = "maintenance"
)

// ScopeIdentity represents the owner and lifecycle generation of a caller (e.g. plugin).
type ScopeIdentity struct {
	Owner      string `json:"owner"`
	Generation uint64 `json:"generation"`
}

func (s ScopeIdentity) IsZero() bool {
	return s.Owner == "" && s.Generation == 0
}

// OccurrenceRef links an execution task to its durable parent occurrence and attempt.
type OccurrenceRef struct {
	JobID        string       `json:"job_id"`
	OccurrenceID OccurrenceID `json:"occurrence_id"`
	AttemptID    AttemptID    `json:"attempt_id"`
	LeaseEpoch   uint64       `json:"lease_epoch"`
}

// Outcome categorizes the final execution status at the physical boundary.
type Outcome string

const (
	OutcomeCompleted          Outcome = "completed"
	OutcomeFailed             Outcome = "failed"
	OutcomeTimedOut           Outcome = "timed_out"
	OutcomeCancelled          Outcome = "cancelled"
	OutcomePanic              Outcome = "panic"
	OutcomeAbortedBeforeStart Outcome = "aborted_before_start"
)

// Cause provides structured explanation for a non-successful outcome or cancellation.
type Cause string

const (
	CauseNone               Cause = "none"
	CauseTimeout            Cause = "timeout"
	CauseUserCancel         Cause = "user_cancel"
	CauseShutdown           Cause = "shutdown"
	CauseQueueExpired       Cause = "queue_expired"
	CauseQuotaExceeded      Cause = "quota_exceeded"
	CausePanic              Cause = "panic"
	CausePersistenceFailure Cause = "persistence_failure"
	CauseScopeClosed        Cause = "scope_closed"
	CauseLeaseLost          Cause = "lease_lost"
)

// FailureInfo holds structured diagnostic details for execution failures.
type FailureInfo struct {
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// TaskSnapshot represents an immutable point-in-time view of task lifecycle state.
type TaskSnapshot struct {
	ID         TaskID        `json:"id"`
	Scope      ScopeIdentity `json:"scope"`
	QuotaOwner OwnerID       `json:"quota_owner"`
	Pool       PoolID        `json:"pool"`
	Class      PriorityClass `json:"class"`
	State      TaskState     `json:"state"`
	AdmittedAt time.Time     `json:"admitted_at"`
	QueuedAt   time.Time     `json:"queued_at"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Error      string        `json:"error,omitempty"`

	CancelRequested  bool  `json:"cancel_requested,omitempty"`
	CancelCause      Cause `json:"cancel_cause,omitempty"`
	LateCancellation bool  `json:"late_cancellation,omitempty"`
}
