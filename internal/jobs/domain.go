package jobs

import (
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// OverlapPolicy controls what happens when a new occurrence is due while a previous occurrence is still active.
type OverlapPolicy string

const (
	OverlapForbid       OverlapPolicy = "forbid"
	OverlapReplace      OverlapPolicy = "replace"
	OverlapAllowBounded OverlapPolicy = "allow_bounded"
)

// MisfirePolicy controls handling of schedules that missed their due time.
type MisfirePolicy string

const (
	MisfireSkip           MisfirePolicy = "skip"
	MisfireRunOnce        MisfirePolicy = "run_once"
	MisfireCatchUpBounded MisfirePolicy = "catch_up_bounded"
)

// OccurrenceState represents the logical lifecycle of a single occurrence.
type OccurrenceState string

const (
	OccurrenceReady      OccurrenceState = "ready"
	OccurrenceDispatched OccurrenceState = "dispatched"
	OccurrenceCompleted  OccurrenceState = "completed"
	OccurrenceFailed     OccurrenceState = "failed"
	OccurrenceCancelled  OccurrenceState = "cancelled"
	OccurrenceBlocked    OccurrenceState = "blocked"
)

// AttemptState represents the physical execution attempt lifecycle.
type AttemptState string

const (
	AttemptLeased             AttemptState = "leased"
	AttemptRunning            AttemptState = "running"
	AttemptCompleted          AttemptState = "completed"
	AttemptFailed             AttemptState = "failed"
	AttemptTimedOut           AttemptState = "timed_out"
	AttemptCancelled          AttemptState = "cancelled"
	AttemptAbortedBeforeStart AttemptState = "aborted_before_start"
	AttemptDeferred           AttemptState = "deferred"
)

// JobRetryPolicy controls retry attempts, exponential backoff, and caps.
type JobRetryPolicy struct {
	MaxAttempts       int           `json:"max_attempts"`
	MaxDeferrals      int           `json:"max_deferrals,omitempty"`
	InitialDelay      time.Duration `json:"initial_delay"`
	MaxDelay          time.Duration `json:"max_delay"`
	BackoffMultiplier float64       `json:"backoff_multiplier"`
}

// JobDefinition represents WHAT work to do, its handler reference, version, policy,
// and explicit execution-resource requirements (ADR 0006 §7.1).
type JobDefinition struct {
	ID          string                      `json:"id"`
	ScopeOwner  string                      `json:"scope_owner"`
	QuotaOwner  string                      `json:"quota_owner"`
	HandlerType string                      `json:"handler_type"`
	Version     int                         `json:"version"`
	Payload     []byte                      `json:"payload,omitempty"`
	Pool        string                      `json:"pool"`
	Class       string                      `json:"class"`
	Timeout     time.Duration               `json:"timeout"`
	Resources   []tasks.ResourceRequirement `json:"resources,omitempty"`
	RetryPolicy JobRetryPolicy              `json:"retry_policy"`
	Enabled     bool                        `json:"enabled"`
	Revision    uint64                      `json:"revision"`
}

// JobSchedule represents WHEN an occurrence is materialized.
type JobSchedule struct {
	ID            string        `json:"id"`
	JobID         string        `json:"job_id"`
	Recurrence    string        `json:"recurrence"`
	Interval      time.Duration `json:"interval"`
	Timezone      string        `json:"timezone"`
	NextDueAt     time.Time     `json:"next_due_at"`
	MisfirePolicy MisfirePolicy `json:"misfire_policy"`
	OverlapPolicy OverlapPolicy `json:"overlap_policy"`
	Enabled       bool          `json:"enabled"`
	Revision      uint64        `json:"revision"`
}

// JobOccurrence represents a single logical run of a job (manual, scheduled, or event-driven).
type JobOccurrence struct {
	ID            string          `json:"id"`
	JobID         string          `json:"job_id"`
	ScheduleID    string          `json:"schedule_id,omitempty"`
	ScheduledFor  time.Time       `json:"scheduled_for"`
	OccurrenceKey string          `json:"occurrence_key"`
	State         OccurrenceState `json:"state"`
	ReadyAt       time.Time       `json:"ready_at"`
	CancelEpoch   uint64          `json:"cancel_epoch"`
	Revision      uint64          `json:"revision"`
}

// JobAttempt represents a single physical execution attempt of an occurrence with a TaskID and lease epoch.
type JobAttempt struct {
	ID           string       `json:"id"`
	OccurrenceID string       `json:"occurrence_id"`
	AttemptNo    int          `json:"attempt_no"`
	TaskID       string       `json:"task_id"`
	LeaseEpoch   uint64       `json:"lease_epoch"`
	LeaseUntil   time.Time    `json:"lease_until"`
	State        AttemptState `json:"state"`
	StartedAt    time.Time    `json:"started_at"`
	FinishedAt   time.Time    `json:"finished_at"`
	Result       []byte       `json:"result,omitempty"`
	Error        string       `json:"error,omitempty"`
}

// OutboxEvent is a durable notification committed with a job state change.
type OutboxEvent struct {
	ID           string
	OccurrenceID string
	Kind         string
	Payload      []byte
	CommittedAt  time.Time
}
