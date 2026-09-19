package tasks

import (
	"time"

	"github.com/inipew/goultroid/internal/execution"
)

// TaskResult represents the immutable outcome of a physical execution attempt (ADR 0006).
type TaskResult struct {
	TaskID      TaskID                `json:"task_id"`
	AttemptID   AttemptID             `json:"attempt_id,omitempty"`
	Outcome     Outcome               `json:"outcome"`
	Cause       Cause                 `json:"cause"`
	Disposition execution.Disposition `json:"disposition,omitempty"`
	StartedAt   time.Time             `json:"started_at"`
	FinishedAt  time.Time             `json:"finished_at"`
	RetryAfter  time.Duration         `json:"retry_after,omitempty"`
	Output      any                   `json:"output,omitempty"`
	Failure     FailureInfo           `json:"failure,omitempty"`
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

// Semantics returns the semantic execution meaning of this physical result.
// Legacy results without an explicit disposition retain historical behavior.
func (r TaskResult) Semantics() execution.Semantics {
	if r.Disposition != "" {
		return execution.Semantics{Disposition: r.Disposition, Code: r.Failure.Code, RetryAfter: r.RetryAfter}
	}
	switch r.Outcome {
	case OutcomeCompleted:
		return execution.Semantics{Disposition: execution.DispositionSuccess}
	case OutcomeCancelled:
		// Legacy durable rows did not distinguish operator cancellation from
		// generic failed attempts; callers needing compatibility should use their
		// persisted metadata fallback. In-memory TaskResult cancellation is final.
		return execution.Semantics{Disposition: execution.DispositionCancelled, Code: string(r.Cause)}
	case OutcomeTimedOut, OutcomeFailed, OutcomePanic, OutcomeAbortedBeforeStart:
		return execution.Semantics{Disposition: execution.DispositionInternal, Code: r.Failure.Code, RetryAfter: r.RetryAfter}
	default:
		return execution.Semantics{Disposition: execution.DispositionInternal, Code: r.Failure.Code}
	}
}

// ShouldRetry applies the default cross-layer retry semantics.
func (r TaskResult) ShouldRetry() bool { return r.Semantics().ShouldRetry() }
