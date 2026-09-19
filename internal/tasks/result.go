package tasks

import (
	"time"
)

// TaskResult represents the immutable outcome of a physical execution attempt (ADR 0006).
type TaskResult struct {
	TaskID     TaskID      `json:"task_id"`
	AttemptID  AttemptID   `json:"attempt_id,omitempty"`
	Outcome    Outcome     `json:"outcome"`
	Cause      Cause       `json:"cause"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	RetryAfter time.Duration `json:"retry_after,omitempty"`
	Output     any           `json:"output,omitempty"`
	Failure    FailureInfo   `json:"failure,omitempty"`
}

// IsSuccess returns true if the task completed normally.
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
