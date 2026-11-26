package logging

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
// Raw Telegram message IDs or user IDs must never be used as correlation IDs.
func NewCorrelationID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("cid-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

// WithCorrelationID attaches a correlation ID to the context, generating one if empty.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		id = NewCorrelationID()
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID extracts the correlation ID from ctx, or returns empty string.
func CorrelationID(ctx context.Context) string {
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
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, causationKey{}, id)
}

// CausationID extracts the causation ID from ctx, or returns empty string.
func CausationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(causationKey{}).(string); ok {
		return v
	}
	return ""
}

// ContextFields returns standard correlation fields present in ctx.
func ContextFields(ctx context.Context) []Field {
	cid := CorrelationID(ctx)
	caid := CausationID(ctx)
	if cid == "" && caid == "" {
		return nil
	}
	fields := make([]Field, 0, 2)
	if cid != "" {
		fields = append(fields, Correlation(cid))
	}
	if caid != "" {
		fields = append(fields, Causation(caid))
	}
	return fields
}
