package core

import (
	"context"
	"time"
)

// WithDefaultTimeout returns a child context with fallback duration if parent does not have a deadline.
// If parent already has a deadline, it preserves the existing deadline and returns a cancellable context.
func WithDefaultTimeout(parent context.Context, fallback time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if _, exists := parent.Deadline(); exists || fallback <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, fallback)
}
