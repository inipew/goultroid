package tasks

import "time"

// TaskResult represents the immutable outcome of a physical execution attempt (ADR 0006).
//
// Cancellation is deliberately modeled as metadata in addition to Outcome.
// A cancellation request is not proof that an external side effect was stopped:
// a handler may observe the request too late and still complete successfully.
// In that case Outcome remains OutcomeCompleted (or the actual failure outcome),
// while CancelRequested/LateCancellation preserve the control-plane history.
type TaskResult struct {
	TaskID     TaskID      `json:"task_id"`
	AttemptID  AttemptID   `json:"attempt_id,omitempty"`
	Outcome    Outcome     `json:"outcome"`
	Cause      Cause       `json:"cause"`
	StartedAt  time.Time   `json:"started_at"`
	FinishedAt time.Time   `json:"finished_at"`
	Output     any         `json:"output,omitempty"`
	Failure    FailureInfo `json:"failure,omitempty"`

	CancelRequested  bool  `json:"cancel_requested,omitempty"`
	CancelCause      Cause `json:"cancel_cause,omitempty"`
	LateCancellation bool  `json:"late_cancellation,omitempty"`
}

// IsSuccess returns true if the task completed normally. A late cancellation
// request does not rewrite an already-observed successful physical outcome.
func (r TaskResult) IsSuccess() bool {
	return r.Outcome == OutcomeCompleted
}

// ExecutionDuration returns elapsed time of the physical execution attempt.
func (r TaskResult) ExecutionDuration() time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	return r.FinishedAt.Sub(r.StartedAt)
}
