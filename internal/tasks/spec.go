package tasks

import (
	"context"
	"errors"
	"strings"
	"time"
)

// HandlerFunc is the physical execution body of a task.
type HandlerFunc func(ctx context.Context) error

// CommitFunc persists the physical execution result to the durable store.
// The engine invokes it exactly once per durability-required task, after
// physical completion, and holds the result credit until it returns (or the
// wait is abandoned). The outcome/cause/failure of res describe the physical
// attempt; the commit must fence on its own lease identity.
type CommitFunc func(ctx context.Context, res TaskResult) error

// ResourceRequirement declares capacity that must be reserved atomically
// before a task is dispatched to a physical worker.
type ResourceRequirement struct {
	Name   string `json:"name"`
	Amount int64  `json:"amount"`
}

// WorkSpec defines an immutable specification for a unit of work submitted to admission (ADR 0006 §5.1).
type WorkSpec struct {
	ID               TaskID                `json:"id"`
	Scope            ScopeIdentity         `json:"scope"`
	QuotaOwner       OwnerID               `json:"quota_owner"`
	Pool             PoolID                `json:"pool"`
	Class            PriorityClass         `json:"class"`
	OrderingKey      string                `json:"ordering_key,omitempty"`
	QueueDeadline    time.Time             `json:"queue_deadline,omitempty"`
	ExecutionTimeout time.Duration         `json:"execution_timeout,omitempty"`
	HandlerRef       string                `json:"handler_ref,omitempty"`
	Input            any                   `json:"input,omitempty"`
	Job              *OccurrenceRef        `json:"job,omitempty"`
	Resources        []ResourceRequirement `json:"resources,omitempty"`

	// Handler is the execution body.
	Handler HandlerFunc `json:"-"`
	// Commit persists the physical result. Non-nil Commit opts the task into
	// the durable commit protocol (CommitPending until acknowledgement).
	// Tasks without Commit (e.g. interactive Telegram work) release their
	// result credit at physical completion.
	Commit CommitFunc `json:"-"`
	// OnComplete receives the final terminal TaskResult.
	OnComplete func(TaskResult) `json:"-"`
}

// RequiresDurability reports whether the task opts into the durable commit
// protocol (CommitPending until the persistence acknowledgement).
func (s *WorkSpec) RequiresDurability() bool {
	return s != nil && s.Commit != nil
}

// Validate checks that required fields on the work specification are present.
func (s *WorkSpec) Validate() error {
	if strings.TrimSpace(string(s.ID)) == "" {
		return errors.New("task id cannot be empty")
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
	switch s.Class {
	case PriorityInteractive, PriorityNormal, PriorityBackground, PriorityMaintenance:
	default:
		return errors.New("invalid priority class")
	}
	if s.ExecutionTimeout < 0 {
		return errors.New("execution timeout cannot be negative")
	}
	seenResources := make(map[string]struct{}, len(s.Resources))
	for _, requirement := range s.Resources {
		if strings.TrimSpace(requirement.Name) == "" || requirement.Amount <= 0 {
			return errors.New("resource requirements need a name and positive amount")
		}
		if _, exists := seenResources[requirement.Name]; exists {
			return errors.New("resource requirement names must be unique")
		}
		seenResources[requirement.Name] = struct{}{}
	}
	return nil
}
