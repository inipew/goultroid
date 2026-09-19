package core

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/execution"
)

var (
	ErrInvocationDenied  = errors.New("command invocation denied")
	ErrPermissionDenied  = errors.New("permission denied")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrForbidden         = errors.New("forbidden")
	ErrValidation        = errors.New("validation failed")
	ErrGroupOnly         = errors.New("command can only be used in groups")
	ErrPrivateOnly       = errors.New("command can only be used in private chat")
	ErrReplyRequired     = errors.New("command must be a reply to a message")
	ErrCooldownActive    = errors.New("command is on cooldown")
	ErrInterceptHandled  = errors.New("message handled by interceptor")
	ErrInvalidArgs       = errors.New("invalid command arguments")
	ErrInvalidArguments  = ErrInvalidArgs
	ErrNotFound          = errors.New("entity not found")
	ErrUnsupported       = errors.New("operation unsupported")
	ErrMedia             = errors.New("media operation failed")
	ErrStorage           = errors.New("storage operation failed")
	ErrTimeout           = errors.New("operation timed out")
	ErrInternal          = errors.New("internal error")
	ErrTelegram          = errors.New("telegram api error")
	ErrRateLimited       = errors.New("rate limited")
	ErrRateLimit         = ErrRateLimited
	ErrPeerUnresolved    = errors.New("peer access hash could not be resolved")
	ErrAccessHashMissing = errors.New("access hash missing for peer")
	ErrPeerInvalid       = errors.New("peer or access hash invalid")
	ErrUnclosedQuote     = errors.New("unclosed quote in command arguments")
	ErrTrailingEscape    = errors.New("trailing backslash escape in command arguments")
	ErrResourceLimit     = errors.New("resource limit exceeded")
	ErrConflict          = errors.New("conflict or concurrent modification")
	ErrUnavailable       = errors.New("service temporarily unavailable")
	ErrLeaseLost         = errors.New("job lease lost or expired")
	ErrCancelled         = errors.New("operation cancelled")
)

type ErrorCategory string

const (
	CategoryNone          ErrorCategory = "none"
	CategoryTransient     ErrorCategory = "transient"
	CategoryPermanent     ErrorCategory = "permanent"
	CategoryRateLimited   ErrorCategory = "rate_limited"
	CategorySecurity      ErrorCategory = "security"
	CategoryInvalidInput  ErrorCategory = "invalid_input"
	CategoryResourceLimit ErrorCategory = "resource_limit"
	CategoryInternal      ErrorCategory = "internal"
)

type RateLimitError struct {
	Wait time.Duration
	Err  error
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return "rate limited"
	}
	if e.Err == nil {
		return fmt.Sprintf("rate limited by telegram: wait %s before retrying", e.Wait)
	}
	return fmt.Sprintf("rate limited by telegram: wait %s before retrying: %v", e.Wait, e.Err)
}

func (e *RateLimitError) Unwrap() error {
	if e == nil {
		return ErrRateLimited
	}
	return errors.Join(ErrRateLimited, e.Err)
}

// RateLimitWait returns the duration requested by the rate limiter.
func (e *RateLimitError) RateLimitWait() time.Duration {
	if e == nil {
		return 0
	}
	return e.Wait
}

func (e *RateLimitError) ExecutionSemantics() execution.Semantics {
	wait := time.Duration(0)
	if e != nil && e.Wait > 0 {
		wait = e.Wait
	}
	return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "rate_limited", RetryAfter: wait}
}

func NewRateLimitError(wait time.Duration, err error) *RateLimitError {
	return &RateLimitError{Wait: wait, Err: err}
}

// IsPermanentError determines whether a scheduler error should not be retried.
func IsPermanentError(err error) bool {
	if err == nil {
		return false
	}
	// Preserve the historical helper contract. The new cross-layer execution
	// semantics intentionally treats resource pressure as retryable, but legacy
	// callers of IsPermanentError have always treated ErrResourceLimit as final.
	if errors.Is(err, ErrResourceLimit) {
		return true
	}
	semantics := ExecutionSemantics(err)
	switch semantics.Disposition {
	case execution.DispositionHandled, execution.DispositionRejected, execution.DispositionPermanent:
		return true
	case execution.DispositionRetryable, execution.DispositionCancelled:
		return false
	}
	text := strings.ToUpper(err.Error())
	for _, marker := range []string{"CHAT_WRITE_FORBIDDEN", "CHANNEL_PRIVATE", "USER_BANNED", "USER_BANNED_IN_CHANNEL", "PEER_ID_INVALID", "USER_ID_INVALID", "CHAT_ID_INVALID", "MESSAGE_ID_INVALID", "SCHEDULED COMMAND NOT FOUND"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

type ErrorWithCategory struct {
	Category ErrorCategory
	Err      error
}

func (e *ErrorWithCategory) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ErrorWithCategory) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ErrorWithCategory) ExecutionSemantics() execution.Semantics {
	if e == nil {
		return execution.Semantics{Disposition: execution.DispositionInternal, Code: "nil_category_error"}
	}
	return semanticsForCategory(e.Category)
}

func WrapCategory(category ErrorCategory, err error) error {
	if err == nil {
		return nil
	}
	return &ErrorWithCategory{Category: category, Err: err}
}

// UsageError represents an expected command usage or argument validation error.
type UsageError struct {
	Message string
}

func (e *UsageError) Error() string {
	if e == nil || e.Message == "" {
		return "invalid command arguments"
	}
	return e.Message
}

func (e *UsageError) Unwrap() error {
	return ErrInvalidArgs
}

func (e *UsageError) ExecutionSemantics() execution.Semantics {
	return execution.Semantics{Disposition: execution.DispositionRejected, Code: "invalid_arguments"}
}

// NewUsageError returns an error indicating invalid or missing command arguments that unwraps to ErrInvalidArgs.
func NewUsageError(msg string) error {
	return &UsageError{Message: msg}
}

func semanticsForCategory(category ErrorCategory) execution.Semantics {
	switch category {
	case CategorySecurity:
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "security_rejected"}
	case CategoryInvalidInput:
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "invalid_input"}
	case CategoryResourceLimit:
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "resource_limit"}
	case CategoryRateLimited:
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "rate_limited"}
	case CategoryTransient:
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "transient"}
	case CategoryPermanent:
		return execution.Semantics{Disposition: execution.DispositionPermanent, Code: "permanent"}
	case CategoryNone:
		return execution.Semantics{Disposition: execution.DispositionSuccess}
	default:
		return execution.Semantics{Disposition: execution.DispositionInternal, Code: "internal"}
	}
}

// ExecutionSemantics maps legacy core errors to the cross-layer execution
// contract while preserving explicit semantics carried by wrapped errors.
func ExecutionSemantics(err error) execution.Semantics {
	if err == nil {
		return execution.Semantics{Disposition: execution.DispositionSuccess}
	}
	if semantics, ok := execution.ExplicitSemantics(err); ok {
		return semantics
	}
	switch {
	case errors.Is(err, ErrInterceptHandled):
		return execution.Semantics{Disposition: execution.DispositionHandled, Code: "intercept_handled"}
	case errors.Is(err, ErrInvocationDenied):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "invocation_denied"}
	case errors.Is(err, ErrPermissionDenied):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "permission_denied"}
	case errors.Is(err, ErrUnauthorized):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "unauthorized"}
	case errors.Is(err, ErrForbidden):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "forbidden"}
	case errors.Is(err, ErrCooldownActive):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "cooldown_active"}
	case errors.Is(err, ErrValidation):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "validation_failed"}
	case errors.Is(err, ErrInvalidArgs):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "invalid_arguments"}
	case errors.Is(err, ErrUnclosedQuote):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "unclosed_quote"}
	case errors.Is(err, ErrTrailingEscape):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "trailing_escape"}
	case errors.Is(err, ErrGroupOnly):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "group_only"}
	case errors.Is(err, ErrPrivateOnly):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "private_only"}
	case errors.Is(err, ErrReplyRequired):
		return execution.Semantics{Disposition: execution.DispositionRejected, Code: "reply_required"}
	case errors.Is(err, ErrRateLimited):
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "rate_limited"}
	case errors.Is(err, ErrResourceLimit):
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "resource_limit"}
	case errors.Is(err, ErrTimeout):
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "timeout"}
	case errors.Is(err, ErrUnavailable):
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "unavailable"}
	case errors.Is(err, ErrConflict):
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "conflict"}
	case errors.Is(err, ErrLeaseLost):
		return execution.Semantics{Disposition: execution.DispositionRetryable, Code: "lease_lost"}
	case errors.Is(err, ErrCancelled):
		return execution.Semantics{Disposition: execution.DispositionCancelled, Code: "cancelled"}
	case errors.Is(err, ErrNotFound):
		return execution.Semantics{Disposition: execution.DispositionPermanent, Code: "not_found"}
	case errors.Is(err, ErrUnsupported):
		return execution.Semantics{Disposition: execution.DispositionPermanent, Code: "unsupported"}
	default:
		return semanticsForCategory(CategoryOf(err))
	}
}

// NormalizeExecutionError attaches typed execution semantics to a legacy core
// error without changing errors.Is/errors.As behavior.
func NormalizeExecutionError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := execution.ExplicitSemantics(err); ok {
		return err
	}
	return execution.WithSemantics(err, ExecutionSemantics(err))
}

func CategoryOf(err error) ErrorCategory {
	if err == nil {
		return CategoryNone
	}
	var c *ErrorWithCategory
	if errors.As(err, &c) && c != nil {
		return c.Category
	}
	if errors.Is(err, ErrInvocationDenied) || errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrForbidden) {
		return CategorySecurity
	}
	if errors.Is(err, ErrValidation) || errors.Is(err, ErrInvalidArgs) || errors.Is(err, ErrUnclosedQuote) || errors.Is(err, ErrTrailingEscape) || errors.Is(err, ErrGroupOnly) || errors.Is(err, ErrPrivateOnly) || errors.Is(err, ErrReplyRequired) {
		return CategoryInvalidInput
	}
	if errors.Is(err, ErrResourceLimit) {
		return CategoryResourceLimit
	}
	if errors.Is(err, ErrRateLimited) {
		return CategoryRateLimited
	}
	if errors.Is(err, ErrTimeout) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrCancelled) || errors.Is(err, ErrConflict) || errors.Is(err, ErrLeaseLost) {
		return CategoryTransient
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnsupported) {
		return CategoryPermanent
	}
	if strings.Contains(strings.ToUpper(err.Error()), "FLOOD_WAIT") {
		return CategoryRateLimited
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") || strings.Contains(strings.ToLower(err.Error()), "connection reset") {
		return CategoryTransient
	}
	if strings.Contains(strings.ToUpper(err.Error()), "USER_BANNED") {
		return CategoryPermanent
	}
	errLower := strings.ToLower(err.Error())
	if strings.HasPrefix(errLower, "missing ") || strings.Contains(errLower, "invalid argument") {
		return CategoryInvalidInput
	}
	return CategoryInternal
}

func ClassifyError(err error) ErrorCategory { return CategoryOf(err) }

func UserMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrValidation) {
		return "⚠️ Validation failed. Please check your input."
	}
	if errors.Is(err, ErrInvocationDenied) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrForbidden) {
		return "⛔ You are not authorized to perform this action."
	}
	if errors.Is(err, ErrGroupOnly) {
		return "⚠️ This command can only be used in groups."
	}
	if errors.Is(err, ErrPrivateOnly) {
		return "⚠️ This command can only be used in private chats."
	}
	if errors.Is(err, ErrReplyRequired) {
		return "⚠️ Reply to a message to use this command."
	}
	if errors.Is(err, ErrCooldownActive) {
		return "⏳ Please wait before using this command again."
	}
	if errors.Is(err, ErrInvalidArgs) {
		return "⚠️ Invalid command arguments."
	}
	if errors.Is(err, ErrNotFound) {
		return "⚠️ Requested item was not found."
	}
	if errors.Is(err, ErrConflict) {
		return "⚠️ Conflict — resource already exists or was modified concurrently."
	}
	if errors.Is(err, ErrUnsupported) {
		return "⚠️ This operation is not supported."
	}
	if errors.Is(err, ErrResourceLimit) {
		return "⚠️ Resource limit reached. Try again later."
	}
	if errors.Is(err, ErrTimeout) || errors.Is(err, ErrUnavailable) {
		return "⚠️ The service is temporarily unavailable."
	}
	if errors.Is(err, ErrRateLimited) {
		return "⏳ Telegram rate limit reached. Please try again later."
	}
	return "❌ An internal error occurred."
}

func IsUserSafeText(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"/home/", "/root/", "token", "password", "secret", "access_hash", "sqlite"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}
