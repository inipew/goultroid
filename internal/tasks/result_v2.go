package tasks

import (
	"errors"
	"time"
)

// LifecycleState is the TaskEngine-owned lifecycle. It is intentionally
// separate from the legacy mutable TaskState during migration.
type LifecycleState uint8

const (
	LifecycleCreated LifecycleState = iota + 1
	LifecycleAdmitted
	LifecycleQueued
	LifecycleDispatching
	LifecycleRunning
	LifecycleSucceeded
	LifecycleFailed
	LifecycleTimedOut
	LifecycleCancelled
	LifecycleExpired
	LifecycleAbortedBeforeStart
	LifecycleRejected
)

func (s LifecycleState) Terminal() bool {
	switch s {
	case LifecycleSucceeded, LifecycleFailed, LifecycleTimedOut, LifecycleCancelled,
		LifecycleExpired, LifecycleAbortedBeforeStart, LifecycleRejected:
		return true
	default:
		return false
	}
}

// Outcome is the immutable physical execution outcome.
type Outcome uint8

const (
	OutcomeSucceeded Outcome = iota + 1
	OutcomeFailed
	OutcomeTimedOut
	OutcomeCancelled
	OutcomeExpired
	OutcomeAbortedBeforeStart
)

func (o Outcome) Valid() bool { return o >= OutcomeSucceeded && o <= OutcomeAbortedBeforeStart }

// ResultCause explains why an outcome occurred without requiring callers to
// parse an error string.
type ResultCause uint8

const (
	ResultCauseNone ResultCause = iota
	ResultCauseHandlerError
	ResultCausePanic
	ResultCauseExecutionTimeout
	ResultCauseCancellation
	ResultCauseQueueDeadline
	ResultCauseScopeClosed
	ResultCauseEngineShutdown
	ResultCausePrepareFailed
	ResultCauseInvalidPermit
)

func (c ResultCause) Valid() bool { return c <= ResultCauseInvalidPermit }

// FailureInfo is a transport-safe failure descriptor. Error implementations
// and stack objects remain inside the execution boundary.
type FailureInfo struct {
	Code    string
	Message string
}

// TaskResultParams is mutable constructor input for immutable TaskResult.
type TaskResultParams struct {
	TaskID     TaskID
	AttemptID  AttemptID
	Outcome    Outcome
	Cause      ResultCause
	StartedAt  time.Time
	FinishedAt time.Time
	Output     ResultRef
	Failure    FailureInfo
}

// TaskResult is the immutable value returned across the worker boundary.
type TaskResult struct {
	taskID     TaskID
	attemptID  AttemptID
	outcome    Outcome
	cause      ResultCause
	startedAt  time.Time
	finishedAt time.Time
	output     ResultRef
	failure    FailureInfo
}

func NewTaskResult(p TaskResultParams) (TaskResult, error) {
	if blank(string(p.TaskID)) {
		return TaskResult{}, errors.New("task ID cannot be empty")
	}
	if !p.Outcome.Valid() {
		return TaskResult{}, errors.New("task outcome is invalid")
	}
	if !p.Cause.Valid() {
		return TaskResult{}, errors.New("result cause is invalid")
	}
	if p.FinishedAt.IsZero() {
		return TaskResult{}, errors.New("finished time is required")
	}
	if !p.StartedAt.IsZero() && p.FinishedAt.Before(p.StartedAt) {
		return TaskResult{}, errors.New("finished time cannot precede started time")
	}
	if p.Outcome == OutcomeSucceeded && p.Cause != ResultCauseNone {
		return TaskResult{}, errors.New("successful result cannot have a failure cause")
	}
	if p.Outcome != OutcomeSucceeded && p.Cause == ResultCauseNone {
		return TaskResult{}, errors.New("non-success result requires a cause")
	}
	var output ResultRef
	if !p.Output.IsZero() {
		output, _ = NewResultRef(p.Output.kind, p.Output.version, p.Output.data)
	}
	return TaskResult{
		taskID:     p.TaskID,
		attemptID:  p.AttemptID,
		outcome:    p.Outcome,
		cause:      p.Cause,
		startedAt:  p.StartedAt,
		finishedAt: p.FinishedAt,
		output:     output,
		failure:    p.Failure,
	}, nil
}

func (r TaskResult) TaskID() TaskID        { return r.taskID }
func (r TaskResult) AttemptID() AttemptID  { return r.attemptID }
func (r TaskResult) Outcome() Outcome      { return r.outcome }
func (r TaskResult) Cause() ResultCause    { return r.cause }
func (r TaskResult) StartedAt() time.Time  { return r.startedAt }
func (r TaskResult) FinishedAt() time.Time { return r.finishedAt }
func (r TaskResult) Output() ResultRef {
	if r.output.IsZero() {
		return ResultRef{}
	}
	copyOutput, _ := NewResultRef(r.output.kind, r.output.version, r.output.data)
	return copyOutput
}
func (r TaskResult) Failure() FailureInfo { return r.failure }

// TaskSnapshot is an immutable observation of coordinator-owned task state.
type TaskSnapshot struct {
	id              TaskID
	state           LifecycleState
	scope           ScopeIdentity
	quotaOwner      QuotaOwner
	pool            PoolID
	attemptID       AttemptID
	createdAt       time.Time
	admittedAt      time.Time
	queuedAt        time.Time
	dispatchingAt   time.Time
	startedAt       time.Time
	finishedAt      time.Time
	cancelRequested bool
}

// TaskSnapshotParams is intended for TaskEngine snapshot construction.
type TaskSnapshotParams struct {
	ID              TaskID
	State           LifecycleState
	Scope           ScopeIdentity
	QuotaOwner      QuotaOwner
	Pool            PoolID
	AttemptID       AttemptID
	CreatedAt       time.Time
	AdmittedAt      time.Time
	QueuedAt        time.Time
	DispatchingAt   time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
	CancelRequested bool
}

func NewTaskSnapshot(p TaskSnapshotParams) (TaskSnapshot, error) {
	if blank(string(p.ID)) {
		return TaskSnapshot{}, errors.New("task ID cannot be empty")
	}
	if p.State < LifecycleCreated || p.State > LifecycleRejected {
		return TaskSnapshot{}, errors.New("task lifecycle state is invalid")
	}
	if p.Scope.IsZero() {
		return TaskSnapshot{}, errors.New("scope identity is required")
	}
	if blank(string(p.QuotaOwner)) {
		return TaskSnapshot{}, errors.New("quota owner cannot be empty")
	}
	if blank(string(p.Pool)) {
		return TaskSnapshot{}, errors.New("pool ID cannot be empty")
	}
	return TaskSnapshot{
		id: p.ID, state: p.State, scope: p.Scope, quotaOwner: p.QuotaOwner,
		pool: p.Pool, attemptID: p.AttemptID, createdAt: p.CreatedAt,
		admittedAt: p.AdmittedAt, queuedAt: p.QueuedAt,
		dispatchingAt: p.DispatchingAt, startedAt: p.StartedAt,
		finishedAt: p.FinishedAt, cancelRequested: p.CancelRequested,
	}, nil
}

func (s TaskSnapshot) ID() TaskID               { return s.id }
func (s TaskSnapshot) State() LifecycleState    { return s.state }
func (s TaskSnapshot) Scope() ScopeIdentity     { return s.scope }
func (s TaskSnapshot) QuotaOwner() QuotaOwner   { return s.quotaOwner }
func (s TaskSnapshot) Pool() PoolID             { return s.pool }
func (s TaskSnapshot) AttemptID() AttemptID     { return s.attemptID }
func (s TaskSnapshot) CreatedAt() time.Time     { return s.createdAt }
func (s TaskSnapshot) AdmittedAt() time.Time    { return s.admittedAt }
func (s TaskSnapshot) QueuedAt() time.Time      { return s.queuedAt }
func (s TaskSnapshot) DispatchingAt() time.Time { return s.dispatchingAt }
func (s TaskSnapshot) StartedAt() time.Time     { return s.startedAt }
func (s TaskSnapshot) FinishedAt() time.Time    { return s.finishedAt }
func (s TaskSnapshot) CancelRequested() bool    { return s.cancelRequested }
