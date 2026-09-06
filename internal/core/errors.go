package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel error classifications based on GoUltroid production error taxonomy.
var (
	// Middleware & security boundaries
	ErrPermissionDenied = errors.New("permission denied")
	ErrGroupOnly        = errors.New("command can only be used in groups")
	ErrPrivateOnly      = errors.New("command can only be used in private chat")
	ErrReplyRequired    = errors.New("command must be a reply to a message")
	ErrCooldownActive   = errors.New("command is on cooldown")

	// Domain & API errors
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

// ErrorCategory classifies errors for telemetry, retry policy, and user presentation.
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

// RateLimitError represents a FloodWait or rate-limiting event from Telegram.
type RateLimitError struct {
	Wait time.Duration
	Err  error
}

// Error returns the human-readable description of the rate limit error.
func (e *RateLimitError) Error() string {
	if e.Wait > 0 {
		return fmt.Sprintf("rate limited by telegram: wait %s before retrying", e.Wait.Round(time.Second))
	}
	if e.Err != nil {
		return fmt.Sprintf("rate limited by telegram: %v", e.Err)
	}
	return "rate limited by telegram"
}

// Unwrap returns the underlying error if available.
func (e *RateLimitError) Unwrap() error {
	return e.Err
}

// Is reports whether this error matches target error.
func (e *RateLimitError) Is(target error) bool {
	return target == ErrRateLimited
}

// NewRateLimitError constructs a new RateLimitError with wait duration and underlying error.
func NewRateLimitError(wait time.Duration, err error) *RateLimitError {
	return &RateLimitError{
		Wait: wait,
		Err:  err,
	}
}

// ClassifyError maps an error to an ErrorCategory for telemetry and decision making.
func ClassifyError(err error) ErrorCategory {
	if err == nil {
		return CategoryNone
	}
	if errors.Is(err, ErrRateLimited) {
		return CategoryRateLimited
	}
	if errors.Is(err, ErrPermissionDenied) {
		return CategorySecurity
	}
	if errors.Is(err, ErrInvalidArgs) || errors.Is(err, ErrUnclosedQuote) || errors.Is(err, ErrTrailingEscape) ||
		errors.Is(err, ErrGroupOnly) || errors.Is(err, ErrPrivateOnly) || errors.Is(err, ErrReplyRequired) {
		return CategoryInvalidInput
	}
	if errors.Is(err, ErrResourceLimit) {
		return CategoryResourceLimit
	}
	if errors.Is(err, ErrTimeout) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrConflict) ||
		errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrCancelled) {
		return CategoryTransient
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnsupported) {
		return CategoryPermanent
	}

	msg := strings.ToUpper(err.Error())
	if strings.Contains(msg, "FLOOD_WAIT") {
		return CategoryRateLimited
	}
	if strings.Contains(msg, "TIMEOUT") || strings.Contains(msg, "DEADLINE") || strings.Contains(msg, "CONNECTION RESET") {
		return CategoryTransient
	}

	permanentPatterns := []string{
		"CHAT_WRITE_FORBIDDEN",
		"CHANNEL_PRIVATE",
		"USER_BANNED",
		"PEER_ID_INVALID",
		"MESSAGE_ID_INVALID",
		"USER_ID_INVALID",
		"CHAT_ID_INVALID",
		"CHAT_ADMIN_REQUIRED",
		"SCHEDULED COMMAND NOT FOUND",
		"SCHEDULED COMMAND PAYLOAD IS NOT A COMMAND",
	}
	for _, p := range permanentPatterns {
		if strings.Contains(msg, p) {
			return CategoryPermanent
		}
	}

	return CategoryInternal
}

// IsPermanentError determines whether an execution error is non-retryable (fatal).
// Permanent errors immediately fail a scheduled job without wasting exponential backoff retries.
func IsPermanentError(err error) bool {
	if err == nil {
		return false
	}
	// Rate limit / flood wait, timeout, and transient service errors are never permanent
	if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrTimeout) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrConflict) || errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrCancelled) {
		return false
	}
	// Sentinel checks
	if errors.Is(err, ErrPermissionDenied) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInvalidArgs) ||
		errors.Is(err, ErrUnsupported) ||
		errors.Is(err, ErrUnclosedQuote) ||
		errors.Is(err, ErrTrailingEscape) ||
		errors.Is(err, ErrResourceLimit) {
		return true
	}
	// RPC string heuristics
	msg := strings.ToUpper(err.Error())
	if strings.Contains(msg, "FLOOD_WAIT") {
		return false
	}
	permanentPatterns := []string{
		"CHAT_WRITE_FORBIDDEN",
		"CHANNEL_PRIVATE",
		"USER_BANNED",
		"PEER_ID_INVALID",
		"MESSAGE_ID_INVALID",
		"USER_ID_INVALID",
		"CHAT_ID_INVALID",
		"CHAT_ADMIN_REQUIRED",
		"SCHEDULED COMMAND NOT FOUND",
		"SCHEDULED COMMAND PAYLOAD IS NOT A COMMAND",
	}
	for _, p := range permanentPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}
