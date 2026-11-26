package logging

import (
	"context"
	"errors"
	"net"
	"strings"
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

// ClassifyError maps an error into a canonical ErrorCategory and retryability flag.
func ClassifyError(err error) (ErrorCategory, bool) {
	if err == nil {
		return CategoryNone, false
	}
	if errors.Is(err, context.Canceled) {
		return CategoryCancellation, false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CategoryCancellation, true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return CategoryNetwork, true
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "flood_wait") || strings.Contains(msg, "rate limit"):
		return CategoryTelegramRPC, true
	case strings.Contains(msg, "peer_id_invalid") || strings.Contains(msg, "telegram"):
		return CategoryTelegramRPC, false
	case strings.Contains(msg, "permission denied") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "forbidden"):
		return CategoryAuthorization, false
	case strings.Contains(msg, "invalid argument") || strings.Contains(msg, "validation") || strings.HasPrefix(msg, "missing "):
		return CategoryValidation, false
	case strings.Contains(msg, "sqlite") || strings.Contains(msg, "database"):
		return CategoryPersistence, strings.Contains(msg, "busy") || strings.Contains(msg, "locked")
	case strings.Contains(msg, "resource limit") || strings.Contains(msg, "capacity") || strings.Contains(msg, "queue full"):
		return CategoryResource, true
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "connection reset") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "eof"):
		return CategoryNetwork, true
	default:
		return CategoryInternal, false
	}
}
