package tasks

import (
	"context"
	"errors"
	"strings"
	"time"
)

// HandlerFunc is the physical execution body of a task.
type HandlerFunc func(ctx context.Context) error

// WorkSpec defines an immutable specification for a unit of work submitted to admission (ADR 0006 §5.1).
type WorkSpec struct {
	ID               TaskID         `json:"id"`
	Scope            ScopeIdentity  `json:"scope"`
	QuotaOwner       OwnerID        `json:"quota_owner"`
	Pool             PoolID         `json:"pool"`
	Class            PriorityClass  `json:"class"`
	OrderingKey      string         `json:"ordering_key,omitempty"`
	QueueDeadline    time.Time      `json:"queue_deadline,omitempty"`
	ExecutionTimeout time.Duration  `json:"execution_timeout,omitempty"`
	HandlerRef       string         `json:"handler_ref,omitempty"`
	Input            any            `json:"input,omitempty"`
	Job              *OccurrenceRef `json:"job,omitempty"`

	// Handler is the execution body.
	Handler HandlerFunc `json:"-"`
	// OnComplete receives the final terminal TaskResult.
	OnComplete func(TaskResult) `json:"-"`
}

// Validate checks that required fields on the work specification are present.
func (s *WorkSpec) Validate() error {
	if strings.TrimSpace(string(s.ID)) == "" {
		return errors.New("task ID cannot be empty")
	}
	if strings.TrimSpace(string(s.QuotaOwner)) == "" {
		return errors.New("quota owner cannot be empty")
	}
	if strings.TrimSpace(string(s.Pool)) == "" {
		return errors.New("pool cannot be empty")
	}
	if s.Handler == nil && strings.TrimSpace(s.HandlerRef) == "" {
		return errors.New("work spec must specify a handler function or handler reference")
	}
	if s.Class == "" {
		s.Class = PriorityNormal
	}
	return nil
}
