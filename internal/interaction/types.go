package interaction

import (
	"context"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type Session struct {
	ID        string
	FeatureID string
	Scope     tasks.ScopeIdentity
	Binding   Binding
	State     []byte
	Revision  uint64
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Resolved couples a session snapshot with its cancellation context.
type Resolved struct {
	Session Session
	Context context.Context
}

// ResolvedCallback is the validated callback-token resolution result.
type ResolvedCallback struct {
	Token   CallbackToken
	Session Session
	Context context.Context
}

// CreateRequest describes a bounded interaction session.
type CreateRequest struct {
	FeatureID string
	Binding   Binding
	State     []byte
	TTL       time.Duration
}

// UpdateRequest atomically replaces session state with optimistic revision checking.
type UpdateRequest struct {
	ExpectedRevision uint64
	State            []byte
	TTL              time.Duration
}

// Stats exposes bounded-retention diagnostics without leaking session contents.
type Stats struct {
	Sessions         int
	StateBytes       int
	Expired          uint64
	Canceled         uint64
	Stale            uint64
	CapacityRejected uint64
}

func normalizeFeatureID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validIdentifier(value string) bool {
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
