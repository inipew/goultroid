package callback

import (
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// ScopeResolver resolves the currently committed lifecycle generation for one
// callback-state owner.
type ScopeResolver func(string) (tasks.ScopeIdentity, bool)

type scopedStateWriter struct {
	base    StateWriter
	owner   string
	resolve ScopeResolver
}

// NewScopedStateWriter returns a fail-closed writer that stamps every opaque
// state item with the producer's current plugin generation.
func NewScopedStateWriter(base StateWriter, owner string, resolve ScopeResolver) StateWriter {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if base == nil || owner == "" || resolve == nil {
		return nil
	}
	return &scopedStateWriter{base: base, owner: owner, resolve: resolve}
}

func (w *scopedStateWriter) Store(data any, allowedUserID int64, ttl time.Duration) string {
	return w.StoreWithScope(data, StateScope{UserID: allowedUserID}, ttl)
}

func (w *scopedStateWriter) StoreWithScope(data any, scope StateScope, ttl time.Duration) string {
	if w == nil || w.base == nil || w.resolve == nil {
		return ""
	}
	ownerScope, ok := w.resolve(w.owner)
	if !ok || ownerScope.IsZero() {
		return ""
	}
	scope.OwnerScope = ownerScope
	return w.base.StoreWithScope(data, scope, ttl)
}
