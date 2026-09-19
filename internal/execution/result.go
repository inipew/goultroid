package execution

import (
	"context"
	"errors"
	"time"
)

// Disposition describes the semantic meaning of an execution result.
// It is orthogonal to physical task outcome/state.
type Disposition string

const (
	DispositionSuccess   Disposition = "success"
	DispositionHandled   Disposition = "handled"
	DispositionRejected  Disposition = "rejected"
	DispositionRetryable Disposition = "retryable"
	DispositionPermanent Disposition = "permanent"
	DispositionCancelled Disposition = "cancelled"
	DispositionInternal  Disposition = "internal"
)

// Semantics carries stable machine-readable meaning across command, task, job,
// scheduler, and transport boundaries.
type Semantics struct {
	Disposition Disposition   `json:"disposition"`
	Code        string        `json:"code,omitempty"`
	RetryAfter  time.Duration `json:"retry_after,omitempty"`
}

func (s Semantics) normalized() Semantics {
	if s.Disposition == "" {
		s.Disposition = DispositionInternal
	}
	if s.RetryAfter < 0 {
		s.RetryAfter = 0
	}
	return s
}

// ShouldRetry is the default durable retry policy for a semantic result.
// Internal failures retain legacy retry behavior; bounded job retry budgets
// prevent infinite loops while preserving compatibility for untyped handlers.
func (s Semantics) ShouldRetry() bool {
	s = s.normalized()
	return s.Disposition == DispositionRetryable || s.Disposition == DispositionInternal
}

func (s Semantics) IsSuccess() bool {
	s = s.normalized()
	return s.Disposition == DispositionSuccess || s.Disposition == DispositionHandled
}

type semanticCarrier interface {
	ExecutionSemantics() Semantics
}

type retryAfterCarrier interface {
	RateLimitWait() time.Duration
}

// Error attaches execution semantics while preserving errors.Is/errors.As
// behavior through Unwrap.
type Error struct {
	Semantics Semantics
	Err       error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Semantics.Code != "" {
		return e.Semantics.Code
	}
	return string(e.Semantics.normalized().Disposition)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *Error) ExecutionSemantics() Semantics {
	if e == nil {
		return Semantics{Disposition: DispositionInternal, Code: "nil_semantic_error"}
	}
	return e.Semantics.normalized()
}

// WithSemantics decorates err with explicit semantics. A nil error remains nil.
func WithSemantics(err error, semantics Semantics) error {
	if err == nil {
		return nil
	}
	// Preserve an already-explicit semantic contract, but allow callers to
	// override semantics that would otherwise be inferred from context or a
	// retry-after interface by wrapping them explicitly here.
	var carrier semanticCarrier
	if errors.As(err, &carrier) && carrier != nil {
		return err
	}
	return &Error{Semantics: semantics.normalized(), Err: err}
}

// ExplicitSemantics extracts semantics carried explicitly by an error,
// context cancellation/deadline, or a rate-limit wait contract.
func ExplicitSemantics(err error) (Semantics, bool) {
	if err == nil {
		return Semantics{Disposition: DispositionSuccess}, true
	}
	var carrier semanticCarrier
	if errors.As(err, &carrier) && carrier != nil {
		return carrier.ExecutionSemantics().normalized(), true
	}
	var rateLimit retryAfterCarrier
	if errors.As(err, &rateLimit) && rateLimit != nil {
		wait := rateLimit.RateLimitWait()
		if wait < 0 {
			wait = 0
		}
		return Semantics{Disposition: DispositionRetryable, Code: "rate_limited", RetryAfter: wait}, true
	}
	if errors.Is(err, context.Canceled) {
		return Semantics{Disposition: DispositionCancelled, Code: "context_cancelled"}, true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Semantics{Disposition: DispositionRetryable, Code: "deadline_exceeded"}, true
	}
	return Semantics{}, false
}

// SemanticsOf always returns a semantic classification. Unknown legacy errors
// are classified Internal and retain bounded retry compatibility.
func SemanticsOf(err error) Semantics {
	if semantics, ok := ExplicitSemantics(err); ok {
		return semantics
	}
	return Semantics{Disposition: DispositionInternal, Code: "unclassified_error"}
}
