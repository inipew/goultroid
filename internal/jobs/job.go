package jobs

import (
	"context"
	"errors"
	"strings"
	"time"
)

// RecoveryPolicy defines the recovery behavior for a persistent job if the runtime was offline.
type RecoveryPolicy string

const (
	RecoveryRunImmediately RecoveryPolicy = "run_immediately"
	RecoverySkip           RecoveryPolicy = "skip"
	RecoveryRecalculate    RecoveryPolicy = "recalculate"
)

// JobState defines the lifecycle state of a job.
type JobState string

const (
	StateRegistered JobState = "registered"
	StateScheduled  JobState = "scheduled"
	StateTriggered  JobState = "triggered"
	StateExecuting  JobState = "executing"
	StateCompleted  JobState = "completed"
	StateFailed     JobState = "failed"
	StateCancelled  JobState = "cancelled"
)

// Job is the declarative representation of scheduled work.
type Job struct {
	ID             string         `json:"id"`
	Owner          string         `json:"owner"`
	Type           string         `json:"type"`
	Schedule       string         `json:"schedule"`
	Payload        []byte         `json:"payload,omitempty"`
	RecoveryPolicy RecoveryPolicy `json:"recovery_policy"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Timeout        time.Duration  `json:"timeout"`
	Pool           string         `json:"pool,omitempty"`

	NextRun   time.Time `json:"next_run"`
	LastRun   time.Time `json:"last_run"`
	State     JobState  `json:"state"`
	LastError string    `json:"last_error,omitempty"`

	Run func(ctx context.Context) error `json:"-"`
}

// Validate checks that the job has required identification and execution fields.
func (j *Job) Validate() error {
	if strings.TrimSpace(j.ID) == "" {
		return errors.New("job ID cannot be empty")
	}
	if strings.TrimSpace(j.Owner) == "" {
		return errors.New("job Owner cannot be empty")
	}
	if j.Run == nil && strings.TrimSpace(j.Type) == "" {
		return errors.New("job must have either a Run handler or a Type")
	}
	return nil
}
