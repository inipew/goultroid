package core

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inipew/goultroid/internal/execution"
)

// UserMessageCarrier marks an error with text explicitly safe to show to a user.
// The internal cause remains available through errors.Is/errors.As.
type UserMessageCarrier interface {
	error
	SafeUserMessage() string
}

type userErrorPresentationCarrier interface {
	UserErrorPresented() bool
}

// UserFacingError separates user-safe presentation from the internal cause.
// Successfully presented failures are semantically handled, preserving the
// historical "user already informed" behavior without hiding the root cause.
type UserFacingError struct {
	cause           error
	userMessage     string
	presented       bool
	presentationErr error
}

func (e *UserFacingError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.cause != nil {
		if e.presentationErr != nil {
			return fmt.Sprintf("%v; present user error: %v", e.cause, e.presentationErr)
		}
		return e.cause.Error()
	}
	if e.presentationErr != nil {
		return e.presentationErr.Error()
	}
	return "user-facing error"
}

func (e *UserFacingError) Unwrap() []error {
	if e == nil {
		return nil
	}
	out := make([]error, 0, 2)
	if e.cause != nil {
		out = append(out, e.cause)
	}
	if e.presentationErr != nil {
		out = append(out, e.presentationErr)
	}
	return out
}

func (e *UserFacingError) SafeUserMessage() string {
	if e == nil {
		return ""
	}
	return e.userMessage
}

func (e *UserFacingError) UserErrorPresented() bool {
	return e != nil && (e.presented || MessageEditMayHaveCommitted(e.presentationErr))
}

func (e *UserFacingError) ExecutionSemantics() execution.Semantics {
	if e != nil && e.UserErrorPresented() {
		code := "user_error_presented"
		if !e.presented {
			code = "user_error_presentation_unconfirmed"
		}
		return execution.Semantics{Disposition: execution.DispositionHandled, Code: code}
	}
	if e == nil || e.cause == nil {
		return execution.Semantics{Disposition: execution.DispositionInternal, Code: "user_error"}
	}
	return ExecutionSemantics(e.cause)
}

func normalizedUserMessage(cause error, message string) string {
	message = strings.TrimSpace(message)
	if message != "" {
		return message
	}
	if cause == nil {
		return UserMessage(ErrInternal)
	}
	return UserMessage(cause)
}

// WithUserMessage attaches an explicitly safe message without presenting it.
func WithUserMessage(cause error, message string) error {
	if cause == nil {
		cause = ErrInternal
	}
	return &UserFacingError{
		cause:       cause,
		userMessage: normalizedUserMessage(cause, message),
	}
}

// ExplicitUserMessage extracts an explicitly safe user message carried by err.
func ExplicitUserMessage(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var carrier UserMessageCarrier
	if !errors.As(err, &carrier) || carrier == nil {
		return "", false
	}
	message := strings.TrimSpace(carrier.SafeUserMessage())
	return message, message != ""
}

// UserErrorWasPresented reports whether safe feedback was delivered or its
// delivery may already have committed. Callers must fail closed in either case
// to avoid duplicate fallback presentation.
func UserErrorWasPresented(err error) bool {
	if err == nil {
		return false
	}
	var carrier userErrorPresentationCarrier
	return errors.As(err, &carrier) && carrier != nil && carrier.UserErrorPresented()
}

// Fail presents only userMessage and returns the internal cause through a typed
// error boundary so metrics/logging retain the real failure without exposing it.
func (c *Context) Fail(cause error, userMessage string) error {
	if cause == nil {
		cause = ErrInternal
	}
	failure := &UserFacingError{
		cause:       cause,
		userMessage: normalizedUserMessage(cause, userMessage),
	}
	if c == nil {
		return failure
	}
	if err := c.Error(failure.userMessage); err != nil {
		failure.presentationErr = err
		return failure
	}
	failure.presented = true
	return failure
}
