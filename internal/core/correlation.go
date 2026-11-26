package core

import (
	"context"

	"github.com/inipew/goultroid/internal/logging"
)

// NewCorrelationID generates a timestamped random correlation ID.
func NewCorrelationID() string {
	return logging.NewCorrelationID()
}

// WithCorrelationID attaches a validated correlation ID to the context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return logging.WithCorrelationID(ctx, id)
}

// GetCorrelationID extracts the correlation ID from the context, or empty string if not present.
func GetCorrelationID(ctx context.Context) string {
	return logging.CorrelationID(ctx)
}

// WithCausationID attaches a validated causation ID to the context.
func WithCausationID(ctx context.Context, id string) context.Context {
	return logging.WithCausationID(ctx, id)
}

// GetCausationID extracts the causation ID from the context, or empty string if not present.
func GetCausationID(ctx context.Context) string {
	return logging.CausationID(ctx)
}
