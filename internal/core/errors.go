package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel error classifications based on GoUltroid production error taxonomy.
var (
	ErrPermissionDenied = errors.New("permission denied")
	ErrGroupOnly        = errors.New("command can only be used in groups")
	ErrPrivateOnly      = errors.New("command can only be used in private chat")
	ErrReplyRequired    = errors.New("command must be a reply to a message")
	ErrCooldownActive   = errors.New("command is on cooldown")
	ErrInterceptHandled = errors.New("message handled by interceptor")

	ErrInvalidArgs    = errors.New("invalid command arguments")
	ErrNotFound       = errors.New("entity not found")
	ErrUnsupported    = errors.New("operation unsupported")
	ErrMedia          = errors.New("media operation failed")
	ErrStorage        = errors.New("storage operation failed")
	ErrTimeout        = errors.New("operation timed out")
	ErrInternal       = errors.New("internal error")
	ErrTelegram       = errors.New("telegram api error")
	ErrRateLimited    = errors.New("rate limited")
	ErrRateLimit      = ErrRateLimited
	ErrPeerUnresolved = errors.New("peer access hash could not be resolved")
	ErrUnclosedQuote  = errors.New("unclosed quote in command arguments")
	ErrTrailingEscape = errors.New("trailing backslash escape in command arguments")
	ErrResourceLimit  = errors.New("resource limit exceeded")
	ErrConflict       = errors.New("conflict or concurrent modification")
	ErrUnavailable    = errors.New("service temporarily unavailable")
	ErrLeaseLost      = errors.New("job lease lost or expired")
	ErrCancelled      = errors.New("operation cancelled")
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
	if e == nil { return "rate limited" }
	if e.Err == nil { return fmt.Sprintf("rate limited for %s", e.Wait) }
	return fmt.Sprintf("rate limited for %s: %v", e.Wait, e.Err)
}
func (e *RateLimitError) Unwrap() error { if e == nil { return nil }; return e.Err }

// NewRateLimitError wraps an API FloodWait in the domain rate-limit type.
func NewRateLimitError(wait time.Duration, err error) error {
	return &RateLimitError{Wait: wait, Err: err}
}

// IsPermanentError classifies errors that should not be retried by the scheduler.
func IsPermanentError(err error) bool {
	if err == nil { return false }
	if errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrInvalidArgs) ||
		errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnsupported) ||
		errors.Is(err, ErrGroupOnly) || errors.Is(err, ErrPrivateOnly) ||
		errors.Is(err, ErrReplyRequired) || errors.Is(err, ErrConflict) {
		return true
	}
	if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrTimeout) ||
		errors.Is(err, ErrUnavailable) || errors.Is(err, ErrTelegram) ||
		errors.Is(err, ErrStorage) || errors.Is(err, ErrInternal) {
		return false
	}
	return false
}

type ErrorWithCategory struct { Category ErrorCategory; Err error }
func (e *ErrorWithCategory) Error() string { if e == nil || e.Err == nil { return "" }; return e.Err.Error() }
func (e *ErrorWithCategory) Unwrap() error { if e == nil { return nil }; return e.Err }

func WrapCategory(category ErrorCategory, err error) error {
	if err == nil { return nil }
	return &ErrorWithCategory{Category: category, Err: err}
}

func CategoryOf(err error) ErrorCategory {
	if err == nil { return CategoryNone }
	var classified *ErrorWithCategory
	if errors.As(err, &classified) && classified != nil { return classified.Category }
	if errors.Is(err, ErrPermissionDenied) { return CategorySecurity }
	if errors.Is(err, ErrInvalidArgs) || errors.Is(err, ErrUnclosedQuote) || errors.Is(err, ErrTrailingEscape) { return CategoryInvalidInput }
	if errors.Is(err, ErrResourceLimit) { return CategoryResourceLimit }
	if errors.Is(err, ErrRateLimited) { return CategoryRateLimited }
	if errors.Is(err, ErrTimeout) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrCancelled) { return CategoryTransient }
	return CategoryInternal
}

func UserMessage(err error) string {
	if err == nil { return "" }
	if errors.Is(err, ErrPermissionDenied) { return "⛔ Permission denied." }
	if errors.Is(err, ErrGroupOnly) { return "⚠️ This command can only be used in groups." }
	if errors.Is(err, ErrPrivateOnly) { return "⚠️ This command can only be used in private chats." }
	if errors.Is(err, ErrReplyRequired) { return "⚠️ Reply to a message to use this command." }
	if errors.Is(err, ErrCooldownActive) { return "⏳ Please wait before using this command again." }
	if errors.Is(err, ErrInvalidArgs) { return "⚠️ Invalid command arguments." }
	if errors.Is(err, ErrNotFound) { return "⚠️ Requested item was not found." }
	if errors.Is(err, ErrUnsupported) { return "⚠️ This operation is not supported." }
	if errors.Is(err, ErrResourceLimit) { return "⚠️ Resource limit reached. Try again later." }
	if errors.Is(err, ErrTimeout) || errors.Is(err, ErrUnavailable) { return "⚠️ The service is temporarily unavailable." }
	if errors.Is(err, ErrRateLimited) { return "⏳ Telegram rate limit reached. Please try again later." }
	return "❌ An internal error occurred."
}

func IsUserSafeText(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"/home/", "/root/", "token", "password", "secret", "access_hash", "sqlite"} {
		if strings.Contains(lower, marker) { return false }
	}
	return true
}
