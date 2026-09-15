package tasks

import (
	"errors"
	"fmt"
)

var (
	ErrEngineQuiescing     = errors.New("task engine is quiescing or stopped")
	ErrOwnerQueueFull      = errors.New("owner queue capacity exceeded")
	ErrPoolBacklogFull     = errors.New("pool backlog limit reached")
	ErrPayloadBudget       = errors.New("payload size exceeds budget")
	ErrResultBackpressure  = errors.New("result capacity saturated")
	ErrDeadlineExpired     = errors.New("queue deadline expired before admission")
	ErrScopeClosed         = errors.New("caller scope is closed")
	ErrUnknownHandler      = errors.New("unknown handler reference")
	ErrLinearizationCancel = errors.New("submission cancelled during admission decision")
	ErrTaskNotFound        = errors.New("task not found")
	ErrPermissionDenied    = errors.New("permission denied")
)

const (
	ReasonOwnerQueueFull      = "owner_queue_full"
	ReasonPoolBacklogFull     = "pool_backlog_full"
	ReasonPayloadBudget       = "payload_budget"
	ReasonResultBackpressure  = "result_backpressure"
	ReasonDeadlineExpired     = "deadline_expired"
	ReasonScopeClosed         = "scope_closed"
	ReasonEngineQuiescing     = "engine_quiescing"
	ReasonUnknownHandler      = "unknown_handler"
	ReasonLinearizationCancel = "linearization_cancel"
)

// AdmissionError carries structured rejection details (ADR 0006 §5.1).
type AdmissionError struct {
	Reason string
	Err    error
}

func (e *AdmissionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("admission rejected (%s): %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("admission rejected: %s", e.Reason)
}

func (e *AdmissionError) Unwrap() error {
	return e.Err
}

func NewAdmissionError(reason string, baseErr error) *AdmissionError {
	return &AdmissionError{
		Reason: reason,
		Err:    baseErr,
	}
}
