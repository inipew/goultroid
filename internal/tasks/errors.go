package tasks

import (
	"errors"
	"fmt"

	"github.com/inipew/goultroid/internal/execution"
)

var (
	ErrEngineQuiescing      = errors.New("task engine is quiescing or stopped")
	ErrOwnerQueueFull       = errors.New("owner queue capacity exceeded")
	ErrPoolBacklogFull      = errors.New("pool backlog limit reached")
	ErrPayloadBudget        = errors.New("payload size exceeds budget")
	ErrUnsupportedPayload   = errors.New("unsupported mutable or opaque task payload")
	ErrResourceUnavailable  = errors.New("required execution resource is unavailable")
	ErrResultBackpressure   = errors.New("result capacity saturated")
	ErrDeliveryBackpressure = errors.New("completion delivery capacity saturated")
	ErrDeadlineExpired      = errors.New("queue deadline expired before admission")
	ErrScopeClosed          = errors.New("caller scope is closed")
	ErrUnknownHandler       = errors.New("unknown handler reference")
	ErrLinearizationCancel  = errors.New("submission cancelled during admission decision")
	ErrRetainedBudget       = errors.New("engine retained memory budget exceeded")
	ErrTaskNotFound         = errors.New("task not found")
	ErrPermissionDenied     = errors.New("permission denied")
)

const (
	ReasonOwnerQueueFull       = "owner_queue_full"
	ReasonPoolBacklogFull      = "pool_backlog_full"
	ReasonPayloadBudget        = "payload_budget"
	ReasonUnsupportedPayload   = "unsupported_payload"
	ReasonResourceUnavailable  = "resource_unavailable"
	ReasonResultBackpressure   = "result_backpressure"
	ReasonDeliveryBackpressure = "delivery_backpressure"
	ReasonDeadlineExpired      = "deadline_expired"
	ReasonScopeClosed          = "scope_closed"
	ReasonEngineQuiescing      = "engine_quiescing"
	ReasonUnknownHandler       = "unknown_handler"
	ReasonLinearizationCancel  = "linearization_cancel"
	ReasonRetainedBudget       = "retained_budget"
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

func (e *AdmissionError) ExecutionSemantics() execution.Semantics {
	if e == nil {
		return execution.Semantics{Disposition: execution.DispositionInternal, Code: "nil_admission_error"}
	}
	switch e.Reason {
	case ReasonPayloadBudget, ReasonUnsupportedPayload, ReasonUnknownHandler:
		return execution.Semantics{Disposition: execution.DispositionPermanent, Code: e.Reason}
	case ReasonScopeClosed, ReasonLinearizationCancel:
		return execution.Semantics{Disposition: execution.DispositionCancelled, Code: e.Reason}
	case ReasonOwnerQueueFull, ReasonPoolBacklogFull, ReasonResourceUnavailable,
		ReasonResultBackpressure, ReasonDeliveryBackpressure, ReasonDeadlineExpired,
		ReasonEngineQuiescing, ReasonRetainedBudget:
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: e.Reason}
	default:
		return execution.Semantics{Disposition: execution.DispositionInternal, Code: e.Reason}
	}
}

func NewAdmissionError(reason string, baseErr error) *AdmissionError {
	return &AdmissionError{
		Reason: reason,
		Err:    baseErr,
	}
}
