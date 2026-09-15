package tasks

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskID identifies exactly one physical execution attempt.
type TaskID string

// JobID identifies one reusable job definition.
type JobID string

// ScheduleID identifies one schedule attached to a job definition.
type ScheduleID string

// OccurrenceID identifies one logical run of a job definition.
type OccurrenceID string

// AttemptID identifies one execution attempt for an occurrence.
type AttemptID string

// ScopeOwner identifies the lifecycle/resource owner of work.
type ScopeOwner string

// QuotaOwner identifies the subject charged for fairness and resource budgets.
type QuotaOwner string

// PoolID identifies a physical worker pool.
type PoolID string

// WorkerID identifies a physical worker slot.
type WorkerID string

// ScopeIdentity fences work to one lifecycle owner generation.
type ScopeIdentity struct {
	owner      ScopeOwner
	generation uint64
}

// NewScopeIdentity creates a validated scope identity.
func NewScopeIdentity(owner ScopeOwner, generation uint64) (ScopeIdentity, error) {
	if blank(string(owner)) {
		return ScopeIdentity{}, errors.New("scope owner cannot be empty")
	}
	if generation == 0 {
		return ScopeIdentity{}, errors.New("scope generation must be positive")
	}
	return ScopeIdentity{owner: owner, generation: generation}, nil
}

func (s ScopeIdentity) Owner() ScopeOwner  { return s.owner }
func (s ScopeIdentity) Generation() uint64 { return s.generation }
func (s ScopeIdentity) IsZero() bool       { return blank(string(s.owner)) || s.generation == 0 }

// PriorityClass is a coarse scheduling class. Every class receives positive
// service weight in the TaskEngine; it is not a caller-selectable privilege.
type PriorityClass uint8

const (
	PriorityInteractive PriorityClass = iota + 1
	PriorityNormal
	PriorityBackground
	PriorityMaintenance
)

func (p PriorityClass) Valid() bool {
	return p >= PriorityInteractive && p <= PriorityMaintenance
}

// SubmissionCause records why finite work entered the execution runtime.
type SubmissionCause uint8

const (
	CauseInteractiveCommand SubmissionCause = iota + 1
	CauseCallback
	CauseInline
	CauseScheduled
	CausePeriodic
	CauseRetry
	CauseMaintenance
	CauseManual
)

func (c SubmissionCause) Valid() bool {
	return c >= CauseInteractiveCommand && c <= CauseManual
}

// HandlerRef is a stable, versioned handler identity. It deliberately contains
// no function pointer or implementation object.
type HandlerRef struct {
	name    string
	version uint16
}

func NewHandlerRef(name string, version uint16) (HandlerRef, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return HandlerRef{}, errors.New("handler name cannot be empty")
	}
	if version == 0 {
		return HandlerRef{}, errors.New("handler version must be positive")
	}
	return HandlerRef{name: name, version: version}, nil
}

func (h HandlerRef) Name() string    { return h.name }
func (h HandlerRef) Version() uint16 { return h.version }
func (h HandlerRef) IsZero() bool    { return h.name == "" || h.version == 0 }

// PayloadRef is an immutable, versioned payload value. Bytes are copied both
// on construction and access so callers cannot mutate accepted work in place.
type PayloadRef struct {
	kind    string
	version uint16
	data    []byte
}

func NewPayloadRef(kind string, version uint16, data []byte) (PayloadRef, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return PayloadRef{}, errors.New("payload kind cannot be empty")
	}
	if version == 0 {
		return PayloadRef{}, errors.New("payload version must be positive")
	}
	return PayloadRef{kind: kind, version: version, data: append([]byte(nil), data...)}, nil
}

func (p PayloadRef) Kind() string    { return p.kind }
func (p PayloadRef) Version() uint16 { return p.version }
func (p PayloadRef) Data() []byte    { return append([]byte(nil), p.data...) }
func (p PayloadRef) Size() int       { return len(p.data) }
func (p PayloadRef) IsZero() bool    { return p.kind == "" || p.version == 0 }

// ResultRef is an immutable, versioned result payload.
type ResultRef struct {
	kind    string
	version uint16
	data    []byte
}

func NewResultRef(kind string, version uint16, data []byte) (ResultRef, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ResultRef{}, errors.New("result kind cannot be empty")
	}
	if version == 0 {
		return ResultRef{}, errors.New("result version must be positive")
	}
	return ResultRef{kind: kind, version: version, data: append([]byte(nil), data...)}, nil
}

func (r ResultRef) Kind() string    { return r.kind }
func (r ResultRef) Version() uint16 { return r.version }
func (r ResultRef) Data() []byte    { return append([]byte(nil), r.data...) }
func (r ResultRef) Size() int       { return len(r.data) }
func (r ResultRef) IsZero() bool    { return r.kind == "" || r.version == 0 }

// OccurrenceRef links a Task to durable job identity without importing the
// jobs package and creating an execution-domain dependency cycle.
type OccurrenceRef struct {
	jobID        JobID
	occurrenceID OccurrenceID
}

func NewOccurrenceRef(jobID JobID, occurrenceID OccurrenceID) (OccurrenceRef, error) {
	if blank(string(jobID)) {
		return OccurrenceRef{}, errors.New("job ID cannot be empty")
	}
	if blank(string(occurrenceID)) {
		return OccurrenceRef{}, errors.New("occurrence ID cannot be empty")
	}
	return OccurrenceRef{jobID: jobID, occurrenceID: occurrenceID}, nil
}

func (r OccurrenceRef) JobID() JobID               { return r.jobID }
func (r OccurrenceRef) OccurrenceID() OccurrenceID { return r.occurrenceID }
func (r OccurrenceRef) IsZero() bool               { return blank(string(r.jobID)) || blank(string(r.occurrenceID)) }

// ResourceRequest declares a resource budget that must be reserved before
// physical execution starts.
type ResourceRequest struct {
	name  string
	units uint32
}

func NewResourceRequest(name string, units uint32) (ResourceRequest, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ResourceRequest{}, errors.New("resource name cannot be empty")
	}
	if units == 0 {
		return ResourceRequest{}, errors.New("resource units must be positive")
	}
	return ResourceRequest{name: name, units: units}, nil
}

func (r ResourceRequest) Name() string  { return r.name }
func (r ResourceRequest) Units() uint32 { return r.units }

// WorkSpecParams is mutable constructor input. NewWorkSpec validates it and
// defensively copies all reference-backed values into an immutable WorkSpec.
type WorkSpecParams struct {
	ID               TaskID
	Scope            ScopeIdentity
	QuotaOwner       QuotaOwner
	Pool             PoolID
	Class            PriorityClass
	Cause            SubmissionCause
	OrderingKey      string
	QueueDeadline    time.Time
	ExecutionTimeout time.Duration
	Handler          HandlerRef
	Input            PayloadRef
	Job              *OccurrenceRef
	Resources        []ResourceRequest
}

// WorkSpec is immutable input for exactly one physical Task attempt.
type WorkSpec struct {
	id               TaskID
	scope            ScopeIdentity
	quotaOwner       QuotaOwner
	pool             PoolID
	class            PriorityClass
	cause            SubmissionCause
	orderingKey      string
	queueDeadline    time.Time
	executionTimeout time.Duration
	handler          HandlerRef
	input            PayloadRef
	job              *OccurrenceRef
	resources        []ResourceRequest
}

func NewWorkSpec(p WorkSpecParams) (WorkSpec, error) {
	if blank(string(p.ID)) {
		return WorkSpec{}, errors.New("task ID cannot be empty")
	}
	if p.Scope.IsZero() {
		return WorkSpec{}, errors.New("scope identity is required")
	}
	if blank(string(p.QuotaOwner)) {
		return WorkSpec{}, errors.New("quota owner cannot be empty")
	}
	if blank(string(p.Pool)) {
		return WorkSpec{}, errors.New("pool ID cannot be empty")
	}
	if !p.Class.Valid() {
		return WorkSpec{}, errors.New("priority class is invalid")
	}
	if !p.Cause.Valid() {
		return WorkSpec{}, errors.New("submission cause is invalid")
	}
	if p.ExecutionTimeout < 0 {
		return WorkSpec{}, errors.New("execution timeout cannot be negative")
	}
	if p.Handler.IsZero() {
		return WorkSpec{}, errors.New("handler reference is required")
	}
	if p.Input.IsZero() {
		return WorkSpec{}, errors.New("payload reference is required")
	}
	if p.Job != nil && p.Job.IsZero() {
		return WorkSpec{}, errors.New("job occurrence reference is invalid")
	}
	for i, resource := range p.Resources {
		if resource.name == "" || resource.units == 0 {
			return WorkSpec{}, fmt.Errorf("resource request %d is invalid", i)
		}
	}

	input, _ := NewPayloadRef(p.Input.kind, p.Input.version, p.Input.data)
	resources := append([]ResourceRequest(nil), p.Resources...)
	var job *OccurrenceRef
	if p.Job != nil {
		copyRef := *p.Job
		job = &copyRef
	}
	return WorkSpec{
		id:               p.ID,
		scope:            p.Scope,
		quotaOwner:       p.QuotaOwner,
		pool:             p.Pool,
		class:            p.Class,
		cause:            p.Cause,
		orderingKey:      p.OrderingKey,
		queueDeadline:    p.QueueDeadline,
		executionTimeout: p.ExecutionTimeout,
		handler:          p.Handler,
		input:            input,
		job:              job,
		resources:        resources,
	}, nil
}

func (s WorkSpec) ID() TaskID                      { return s.id }
func (s WorkSpec) Scope() ScopeIdentity            { return s.scope }
func (s WorkSpec) QuotaOwner() QuotaOwner          { return s.quotaOwner }
func (s WorkSpec) Pool() PoolID                    { return s.pool }
func (s WorkSpec) Class() PriorityClass            { return s.class }
func (s WorkSpec) Cause() SubmissionCause          { return s.cause }
func (s WorkSpec) OrderingKey() string             { return s.orderingKey }
func (s WorkSpec) QueueDeadline() time.Time        { return s.queueDeadline }
func (s WorkSpec) ExecutionTimeout() time.Duration { return s.executionTimeout }

// WithExecutionTimeout returns a copy with an explicit execution deadline.
// TaskEngine uses this to materialize its validated default without mutating
// the caller-owned immutable WorkSpec.
func (s WorkSpec) WithExecutionTimeout(timeout time.Duration) (WorkSpec, error) {
	if timeout <= 0 {
		return WorkSpec{}, errors.New("execution timeout must be positive")
	}
	copySpec := s
	copySpec.executionTimeout = timeout
	return copySpec, nil
}
func (s WorkSpec) Handler() HandlerRef { return s.handler }
func (s WorkSpec) Input() PayloadRef {
	copyInput, _ := NewPayloadRef(s.input.kind, s.input.version, s.input.data)
	return copyInput
}
func (s WorkSpec) Job() (OccurrenceRef, bool) {
	if s.job == nil {
		return OccurrenceRef{}, false
	}
	return *s.job, true
}
func (s WorkSpec) Resources() []ResourceRequest {
	return append([]ResourceRequest(nil), s.resources...)
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }
