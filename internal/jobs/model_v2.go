package jobs

import (
	"errors"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// Durability defines whether a definition is reconstructed from persistent
// storage or exists only for the current runtime generation.
type Durability uint8

const (
	DurabilityEphemeral Durability = iota + 1
	DurabilityPersistent
)

// OverlapPolicy controls creation of concurrent occurrences for one definition.
type OverlapPolicy uint8

const (
	OverlapAllow OverlapPolicy = iota + 1
	OverlapForbid
)

// RetryPolicyV2 is attempt policy; retry creates a new AttemptID and TaskID for
// the same OccurrenceID.
type RetryPolicyV2 struct {
	MaxAttempts uint16
	Delay       time.Duration
}

// JobPolicy is declarative policy owned by JobManager.
type JobPolicy struct {
	Retry      RetryPolicyV2
	Overlap    OverlapPolicy
	Durability Durability
}

// JobDefinitionParams is mutable constructor input for an immutable definition.
type JobDefinitionParams struct {
	ID         tasks.JobID
	Scope      tasks.ScopeIdentity
	QuotaOwner tasks.QuotaOwner
	Pool       tasks.PoolID
	Class      tasks.PriorityClass
	Handler    tasks.HandlerRef
	Payload    tasks.PayloadRef
	Policy     JobPolicy
}

// JobDefinition describes what to execute. It contains references and payload,
// never a handler closure or transport object.
type JobDefinition struct {
	id         tasks.JobID
	scope      tasks.ScopeIdentity
	quotaOwner tasks.QuotaOwner
	pool       tasks.PoolID
	class      tasks.PriorityClass
	handler    tasks.HandlerRef
	payload    tasks.PayloadRef
	policy     JobPolicy
}

func NewJobDefinition(p JobDefinitionParams) (JobDefinition, error) {
	if strings.TrimSpace(string(p.ID)) == "" {
		return JobDefinition{}, errors.New("job ID cannot be empty")
	}
	if p.Scope.IsZero() {
		return JobDefinition{}, errors.New("scope identity is required")
	}
	if strings.TrimSpace(string(p.QuotaOwner)) == "" || strings.TrimSpace(string(p.Pool)) == "" {
		return JobDefinition{}, errors.New("quota owner and pool are required")
	}
	if !p.Class.Valid() || p.Handler.IsZero() || p.Payload.IsZero() {
		return JobDefinition{}, errors.New("job execution reference is invalid")
	}
	if p.Policy.Durability != DurabilityEphemeral && p.Policy.Durability != DurabilityPersistent {
		return JobDefinition{}, errors.New("job durability is invalid")
	}
	if p.Policy.Overlap != OverlapAllow && p.Policy.Overlap != OverlapForbid {
		return JobDefinition{}, errors.New("job overlap policy is invalid")
	}
	if p.Policy.Retry.MaxAttempts == 0 || p.Policy.Retry.Delay < 0 {
		return JobDefinition{}, errors.New("job retry policy is invalid")
	}
	payload, _ := tasks.NewPayloadRef(p.Payload.Kind(), p.Payload.Version(), p.Payload.Data())
	return JobDefinition{
		id: p.ID, scope: p.Scope, quotaOwner: p.QuotaOwner, pool: p.Pool,
		class: p.Class, handler: p.Handler, payload: payload, policy: p.Policy,
	}, nil
}

func (d JobDefinition) ID() tasks.JobID              { return d.id }
func (d JobDefinition) Scope() tasks.ScopeIdentity   { return d.scope }
func (d JobDefinition) QuotaOwner() tasks.QuotaOwner { return d.quotaOwner }
func (d JobDefinition) Pool() tasks.PoolID           { return d.pool }
func (d JobDefinition) Class() tasks.PriorityClass   { return d.class }
func (d JobDefinition) Handler() tasks.HandlerRef    { return d.handler }
func (d JobDefinition) Payload() tasks.PayloadRef {
	p, _ := tasks.NewPayloadRef(d.payload.Kind(), d.payload.Version(), d.payload.Data())
	return p
}
func (d JobDefinition) Policy() JobPolicy { return d.policy }

// ScheduleKind is intentionally small in P1; adapters translate existing
// schedule formats into one-shot or recurring schedule values.
type ScheduleKind uint8

const (
	ScheduleOnce ScheduleKind = iota + 1
	ScheduleInterval
)

// JobSchedule is an immutable timing definition; Scheduler receives only its
// deadline reference and generation, never the feature handler.
type JobSchedule struct {
	id         tasks.ScheduleID
	jobID      tasks.JobID
	kind       ScheduleKind
	next       time.Time
	interval   time.Duration
	generation uint64
}

func NewJobSchedule(id tasks.ScheduleID, jobID tasks.JobID, kind ScheduleKind, next time.Time, interval time.Duration, generation uint64) (JobSchedule, error) {
	if strings.TrimSpace(string(id)) == "" || strings.TrimSpace(string(jobID)) == "" {
		return JobSchedule{}, errors.New("schedule and job IDs are required")
	}
	if next.IsZero() || generation == 0 {
		return JobSchedule{}, errors.New("schedule deadline and generation are required")
	}
	if kind != ScheduleOnce && kind != ScheduleInterval {
		return JobSchedule{}, errors.New("schedule kind is invalid")
	}
	if kind == ScheduleInterval && interval <= 0 {
		return JobSchedule{}, errors.New("interval schedule requires positive interval")
	}
	if kind == ScheduleOnce && interval != 0 {
		return JobSchedule{}, errors.New("one-shot schedule cannot have interval")
	}
	return JobSchedule{id: id, jobID: jobID, kind: kind, next: next, interval: interval, generation: generation}, nil
}

func (s JobSchedule) ID() tasks.ScheduleID    { return s.id }
func (s JobSchedule) JobID() tasks.JobID      { return s.jobID }
func (s JobSchedule) Kind() ScheduleKind      { return s.kind }
func (s JobSchedule) Next() time.Time         { return s.next }
func (s JobSchedule) Interval() time.Duration { return s.interval }
func (s JobSchedule) Generation() uint64      { return s.generation }

// OccurrenceCause distinguishes recurring ticks, manual triggers, and events.
type OccurrenceCause uint8

const (
	OccurrenceScheduled OccurrenceCause = iota + 1
	OccurrenceManual
	OccurrenceEvent
)

// JobOccurrence is one logical requested run. Retries never replace this ID.
type JobOccurrence struct {
	id        tasks.OccurrenceID
	jobID     tasks.JobID
	cause     OccurrenceCause
	createdAt time.Time
	dueAt     time.Time
}

func NewJobOccurrence(id tasks.OccurrenceID, jobID tasks.JobID, cause OccurrenceCause, createdAt, dueAt time.Time) (JobOccurrence, error) {
	if strings.TrimSpace(string(id)) == "" || strings.TrimSpace(string(jobID)) == "" {
		return JobOccurrence{}, errors.New("occurrence and job IDs are required")
	}
	if cause < OccurrenceScheduled || cause > OccurrenceEvent || createdAt.IsZero() {
		return JobOccurrence{}, errors.New("occurrence cause and creation time are required")
	}
	if cause == OccurrenceScheduled && dueAt.IsZero() {
		return JobOccurrence{}, errors.New("scheduled occurrence requires due time")
	}
	return JobOccurrence{id: id, jobID: jobID, cause: cause, createdAt: createdAt, dueAt: dueAt}, nil
}

func (o JobOccurrence) ID() tasks.OccurrenceID { return o.id }
func (o JobOccurrence) JobID() tasks.JobID     { return o.jobID }
func (o JobOccurrence) Cause() OccurrenceCause { return o.cause }
func (o JobOccurrence) CreatedAt() time.Time   { return o.createdAt }
func (o JobOccurrence) DueAt() time.Time       { return o.dueAt }

// AttemptState is JobManager-owned durable attempt state.
type AttemptState uint8

const (
	AttemptPrepared AttemptState = iota + 1
	AttemptRunning
	AttemptCommitPending
	AttemptCommitted
	AttemptAbortedBeforeStart
	AttemptRecoveryRequired
)

// JobAttempt is one concrete try for an occurrence. Retry creates another
// JobAttempt rather than mutating this identity into another execution.
type JobAttempt struct {
	id           tasks.AttemptID
	occurrenceID tasks.OccurrenceID
	taskID       tasks.TaskID
	number       uint16
	leaseEpoch   uint64
	state        AttemptState
}

func NewJobAttempt(id tasks.AttemptID, occurrenceID tasks.OccurrenceID, taskID tasks.TaskID, number uint16, leaseEpoch uint64, state AttemptState) (JobAttempt, error) {
	if strings.TrimSpace(string(id)) == "" || strings.TrimSpace(string(occurrenceID)) == "" || strings.TrimSpace(string(taskID)) == "" {
		return JobAttempt{}, errors.New("attempt, occurrence, and task IDs are required")
	}
	if number == 0 || leaseEpoch == 0 {
		return JobAttempt{}, errors.New("attempt number and lease epoch must be positive")
	}
	if state < AttemptPrepared || state > AttemptRecoveryRequired {
		return JobAttempt{}, errors.New("attempt state is invalid")
	}
	return JobAttempt{id: id, occurrenceID: occurrenceID, taskID: taskID, number: number, leaseEpoch: leaseEpoch, state: state}, nil
}

func (a JobAttempt) ID() tasks.AttemptID              { return a.id }
func (a JobAttempt) OccurrenceID() tasks.OccurrenceID { return a.occurrenceID }
func (a JobAttempt) TaskID() tasks.TaskID             { return a.taskID }
func (a JobAttempt) Number() uint16                   { return a.number }
func (a JobAttempt) LeaseEpoch() uint64               { return a.leaseEpoch }
func (a JobAttempt) State() AttemptState              { return a.state }
