package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrAdmissionRejected = errors.New("task admission rejected")

// RejectionReason is a structured admission decision reason.
type RejectionReason string

const (
	RejectOwnerQueueFull  RejectionReason = "owner_queue_full"
	RejectPoolBacklogFull RejectionReason = "pool_backlog_full"
	RejectPayloadBudget   RejectionReason = "payload_budget"
	RejectResultBackpress RejectionReason = "result_backpressure"
	RejectDeadlineExpired RejectionReason = "deadline_expired"
	RejectScopeClosed     RejectionReason = "scope_closed"
	RejectEngineQuiescing RejectionReason = "engine_quiescing"
	RejectInvalidSpec     RejectionReason = "invalid_spec"
)

// AdmissionError is returned only when the request did not become accepted.
type AdmissionError struct {
	Reason RejectionReason
}

func (e *AdmissionError) Error() string {
	return fmt.Sprintf("%s: %s", ErrAdmissionRejected, e.Reason)
}
func (e *AdmissionError) Unwrap() error { return ErrAdmissionRejected }

// AdmissionTicket proves that TaskEngine accepted a WorkSpec and reserved all
// admission-time budgets, including one result credit.
type AdmissionTicket struct {
	taskID     TaskID
	acceptedAt time.Time
}

func NewAdmissionTicket(taskID TaskID, acceptedAt time.Time) (AdmissionTicket, error) {
	if blank(string(taskID)) {
		return AdmissionTicket{}, errors.New("ticket task ID cannot be empty")
	}
	if acceptedAt.IsZero() {
		return AdmissionTicket{}, errors.New("ticket accepted time is required")
	}
	return AdmissionTicket{taskID: taskID, acceptedAt: acceptedAt}, nil
}
func (t AdmissionTicket) TaskID() TaskID        { return t.taskID }
func (t AdmissionTicket) AcceptedAt() time.Time { return t.acceptedAt }

// CancelReason is why cancellation was requested. Cancellation is a request,
// not proof that a running handler stopped or rolled back side effects.
type CancelReason string

const (
	CancelCaller      CancelReason = "caller"
	CancelScopeClosed CancelReason = "scope_closed"
	CancelShutdown    CancelReason = "shutdown"
	CancelSuperseded  CancelReason = "superseded"
	CancelOperator    CancelReason = "operator"
)

// CancelReceipt reports the coordinator state observed after a cancel request.
type CancelReceipt struct {
	TaskID          TaskID
	Requested       bool
	AlreadyTerminal bool
	State           LifecycleState
}

// Client is the narrow producer contract. Submit waits for an admission
// decision only; it never waits for a worker slot or task completion.
type Client interface {
	Submit(context.Context, WorkSpec) (AdmissionTicket, error)
	Cancel(TaskID, CancelReason) (CancelReceipt, error)
	Snapshot(TaskID) (TaskSnapshot, bool)
}

// PhysicalPermit represents one real worker slot reservation and is valid for
// one task, worker generation, and dispatch epoch only.
type PhysicalPermit struct {
	pool             PoolID
	workerID         WorkerID
	workerGeneration uint64
	taskID           TaskID
	dispatchEpoch    uint64
}

func NewPhysicalPermit(pool PoolID, worker WorkerID, workerGeneration uint64, taskID TaskID, dispatchEpoch uint64) (PhysicalPermit, error) {
	if blank(string(pool)) || blank(string(worker)) || blank(string(taskID)) {
		return PhysicalPermit{}, errors.New("permit pool, worker, and task IDs are required")
	}
	if workerGeneration == 0 || dispatchEpoch == 0 {
		return PhysicalPermit{}, errors.New("permit generation and dispatch epoch must be positive")
	}
	return PhysicalPermit{pool: pool, workerID: worker, workerGeneration: workerGeneration, taskID: taskID, dispatchEpoch: dispatchEpoch}, nil
}
func (p PhysicalPermit) Pool() PoolID             { return p.pool }
func (p PhysicalPermit) WorkerID() WorkerID       { return p.workerID }
func (p PhysicalPermit) WorkerGeneration() uint64 { return p.workerGeneration }
func (p PhysicalPermit) TaskID() TaskID           { return p.taskID }
func (p PhysicalPermit) DispatchEpoch() uint64    { return p.dispatchEpoch }
func (p PhysicalPermit) IsZero() bool {
	return blank(string(p.pool)) || blank(string(p.workerID)) || blank(string(p.taskID)) || p.workerGeneration == 0 || p.dispatchEpoch == 0
}

// WorkerAssignment is immutable handoff data for one physical attempt.
type WorkerAssignment struct {
	permit PhysicalPermit
	spec   WorkSpec
}

func NewWorkerAssignment(permit PhysicalPermit, spec WorkSpec) (WorkerAssignment, error) {
	if permit.IsZero() {
		return WorkerAssignment{}, errors.New("physical permit is required")
	}
	if permit.TaskID() != spec.ID() {
		return WorkerAssignment{}, errors.New("permit task ID does not match work spec")
	}
	if permit.Pool() != spec.Pool() {
		return WorkerAssignment{}, errors.New("permit pool does not match work spec")
	}
	return WorkerAssignment{permit: permit, spec: spec}, nil
}
func (a WorkerAssignment) Permit() PhysicalPermit { return a.permit }
func (a WorkerAssignment) Spec() WorkSpec         { return a.spec }

// WorkerDispatcher is implemented by the physical worker subsystem. It must
// not maintain a second logical backlog; an accepted assignment already owns
// a concrete physical permit.
type WorkerDispatcher interface {
	Assign(context.Context, WorkerAssignment) error
}

// WorkerEvents is the result boundary back into TaskEngine. Implementations
// must never execute feature callbacks inline from these methods.
type WorkerEvents interface {
	Started(PhysicalPermit, time.Time) error
	Completed(PhysicalPermit, TaskResult) error
}

// PrepareRequest asks the durable owner to commit an attempt before handoff.
type PrepareRequest struct {
	Ticket     AdmissionTicket
	Occurrence OccurrenceRef
	Permit     PhysicalPermit
	Deadline   time.Time
}

// PrepareGrant is durable evidence that an attempt may run. AttemptID is
// assigned only by the durable Job owner after prepare commit.
type PrepareGrant struct {
	AttemptID  AttemptID
	LeaseEpoch uint64
}

// DurablePreparePort is consumed by TaskEngine. Deferred prepare must not
// consume execution retry budget and must allow the physical permit to return.
type DurablePreparePort interface {
	Prepare(context.Context, PrepareRequest) (PrepareGrant, error)
}

// CompletionAck separates physical completion from durable persistence.
type CompletionAck struct {
	AttemptID        AttemptID
	Committed        bool
	RecoveryRequired bool
}

// DurableCompletionPort acknowledges whether a physical result is durably
// committed. Result credit remains held until this protocol finishes.
type DurableCompletionPort interface {
	CommitResult(context.Context, PrepareGrant, TaskResult) (CompletionAck, error)
}
