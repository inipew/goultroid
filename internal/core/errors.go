package core

import (
	"errors"
	"fmt"
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
	ErrInvalidArgs   = errors.New("invalid command arguments")
	ErrNotFound      = errors.New("entity not found")
	ErrUnsupported   = errors.New("operation unsupported")
	ErrMedia         = errors.New("media operation failed")
	ErrStorage       = errors.New("storage operation failed")
	ErrTimeout       = errors.New("operation timed out")
	ErrInternal      = errors.New("internal error")
	ErrTelegram        = errors.New("telegram api error")
	ErrRateLimited     = errors.New("rate limited")
	ErrPeerUnresolved  = errors.New("peer access hash could not be resolved")
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
