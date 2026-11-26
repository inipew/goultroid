package logging

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

// ErrorCategory represents a canonical operational error classification.
type ErrorCategory string

const (
	CategoryNone          ErrorCategory = "none"
	CategoryValidation    ErrorCategory = "validation"
	CategoryAuthorization ErrorCategory = "authorization"
	CategoryTelegramRPC   ErrorCategory = "telegram_rpc"
	CategoryNetwork       ErrorCategory = "network"
	CategoryPersistence   ErrorCategory = "persistence"
	CategoryResource      ErrorCategory = "resource"
	CategoryCancellation  ErrorCategory = "cancellation"
	CategoryInternal      ErrorCategory = "internal"
)

// CategoryProvider allows domain/core error types to expose an explicit
// logging category and retryability flag without string matching.
type CategoryProvider interface {
	LoggingCategory() (ErrorCategory, bool)
}

type rateLimitWaiter interface {
	RateLimitWait() time.Duration
}

// ClassifyError maps an error into a canonical ErrorCategory and retryability flag,
// prioritizing typed/sentinel errors over string heuristics.
func ClassifyError(err error) (ErrorCategory, bool) {
	if err == nil {
		return CategoryNone, false
	}

	var provider CategoryProvider
	if errors.As(err, &provider) && provider != nil {
		return provider.LoggingCategory()
	}

	if errors.Is(err, context.Canceled) {
		return CategoryCancellation, false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// DeadlineExceeded is not universally safe to retry without caller context.
		return CategoryCancellation, false
	}

	var rl rateLimitWaiter
	if errors.As(err, &rl) {
		return CategoryTelegramRPC, true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return CategoryNetwork, true
	}

	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return CategoryAuthorization, false
	}
	if errors.Is(err, os.ErrInvalid) {
		return CategoryValidation, false
	}

	// Narrow fallback for legacy untyped errors (e.g. raw Telegram RPC codes).
	upper := strings.ToUpper(err.Error())
	if strings.Contains(upper, "FLOOD_WAIT") {
		return CategoryTelegramRPC, true
	}
	if strings.Contains(upper, "PEER_ID_INVALID") || strings.Contains(upper, "USER_BANNED") {
		return CategoryTelegramRPC, false
	}
	return CategoryInternal, false
}
