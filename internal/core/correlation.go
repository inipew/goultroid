package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type correlationKey struct{}
type causationKey struct{}

// NewCorrelationID generates a timestamped random correlation ID.
func NewCorrelationID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("cid-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

// WithCorrelationID attaches a correlation ID to the context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		id = NewCorrelationID()
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

// GetCorrelationID extracts the correlation ID from the context, or empty string if not present.
func GetCorrelationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(correlationKey{}).(string); ok {
		return v
	}
	return ""
}

// WithCausationID attaches a causation ID to the context.
func WithCausationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, causationKey{}, id)
}

// GetCausationID extracts the causation ID from the context, or empty string if not present.
func GetCausationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(causationKey{}).(string); ok {
		return v
	}
	return ""
}
