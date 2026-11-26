package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

const (
	// MaxCorrelationIDLen bounds correlation and causation IDs to prevent log/memory bloat.
	MaxCorrelationIDLen = 64
)

type correlationKey struct{}
type causationKey struct{}

var (
	fallbackCorrSeq atomic.Uint64
	entropyReader   io.Reader = rand.Reader
)

// IsValidCorrelationID checks that id is non-empty, within MaxCorrelationIDLen,
// and contains only safe ASCII identifier characters [A-Za-z0-9._-].
func IsValidCorrelationID(id string) bool {
	if len(id) == 0 || len(id) > MaxCorrelationIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// NewCorrelationID generates a timestamped random correlation ID.
// If the OS entropy source fails, it falls back to a monotonic atomic sequence.
// Raw Telegram message IDs or user IDs must never be used as correlation IDs.
func NewCorrelationID() string {
	return newCorrelationIDFrom(entropyReader)
}

func newCorrelationIDFrom(r io.Reader) string {
	var b [8]byte
	if r != nil {
		if _, err := io.ReadFull(r, b[:]); err == nil {
			return fmt.Sprintf("cid-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))
		}
	}
	seq := fallbackCorrSeq.Add(1)
	return fmt.Sprintf("cid-%d-seq%d", time.Now().UnixNano(), seq)
}

// WithCorrelationID attaches a validated correlation ID to the context.
// If id is empty or invalid (contains control chars, spaces, or exceeds MaxCorrelationIDLen),
// a fresh correlation ID is generated.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !IsValidCorrelationID(id) {
		id = NewCorrelationID()
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID extracts the correlation ID from ctx, or returns empty string.
func CorrelationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(correlationKey{}).(string); ok && IsValidCorrelationID(v) {
		return v
	}
	return ""
}

// WithCausationID attaches a validated causation ID to the context.
// Invalid IDs are ignored to protect log sinks from untrusted control characters.
func WithCausationID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !IsValidCorrelationID(id) {
		return ctx
	}
	return context.WithValue(ctx, causationKey{}, id)
}

// CausationID extracts the causation ID from ctx, or returns empty string.
func CausationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(causationKey{}).(string); ok && IsValidCorrelationID(v) {
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
