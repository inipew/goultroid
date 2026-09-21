package interaction

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

// Resolve validates expiry, current plugin generation, and actor/target bindings.
func (r *Runtime) Resolve(ctx context.Context, id string, actual Binding) (Resolved, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	actual = actual.normalized()
	if err := actual.Validate(); err != nil {
		return Resolved{}, err
	}
	resolved, err := r.currentSession(id)
	if err != nil {
		return Resolved{}, err
	}
	if !resolved.Session.Binding.Matches(actual) {
		return Resolved{}, ErrBindingMismatch
	}
	return resolved, nil
}

// UpdateState atomically replaces opaque state and advances the state revision.
// A positive TTL refreshes expiry; zero preserves the existing deadline.
func (r *Runtime) UpdateState(ctx context.Context, id string, request UpdateRequest) (Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if len(request.State) > r.config.MaxStateBytes {
		return Session{}, ErrStateTooLarge
	}
	if request.ExpectedRevision == 0 {
		return Session{}, ErrRevisionConflict
	}
	var ttl time.Duration
	var err error
	if request.TTL != 0 {
		ttl, err = r.normalizeTTL(request.TTL)
		if err != nil {
			return Session{}, err
		}
	}

	resolved, err := r.currentSessionMetadata(id)
	if err != nil {
		return Session{}, err
	}

	r.mu.Lock()
	entry, ok := r.sessions[id]
	if !ok || entry.session.Scope != resolved.Session.Scope {
		r.mu.Unlock()
		return Session{}, ErrNotFound
	}
	now := r.now()
	if !entry.session.ExpiresAt.After(now) {
		r.removeLocked(id, ErrExpired)
		r.mu.Unlock()
		return Session{}, ErrExpired
	}
	if entry.session.Revision != request.ExpectedRevision {
		r.mu.Unlock()
		return Session{}, ErrRevisionConflict
	}
	newTotal := r.stateBytes - len(entry.session.State) + len(request.State)
	if newTotal > r.config.MaxTotalStateBytes {
		r.capacityRejectedCount++
		r.mu.Unlock()
		return Session{}, ErrCapacity
	}
	r.stateBytes = newTotal
	entry.session.State = append([]byte(nil), request.State...)
	entry.session.Revision++
	if request.TTL != 0 {
		entry.session.ExpiresAt = now.Add(ttl)
		r.scheduleExpiryLocked(entry, entry.session.ExpiresAt)
	}
	snapshot := cloneSession(entry.session)
	r.mu.Unlock()
	return snapshot, nil
}

// BindTarget attaches the concrete message target after rendering. It does not
// advance state revision, so callback data generated before send remains valid.
func (r *Runtime) BindTarget(ctx context.Context, id string, expectedRevision uint64, target TargetBinding) (Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if expectedRevision == 0 {
		return Session{}, ErrRevisionConflict
	}
	if err := target.validateComplete(); err != nil {
		return Session{}, err
	}
	resolved, err := r.currentSessionMetadata(id)
	if err != nil {
		return Session{}, err
	}

	r.mu.Lock()
	entry, ok := r.sessions[id]
	if !ok || entry.session.Scope != resolved.Session.Scope {
		r.mu.Unlock()
		return Session{}, ErrNotFound
	}
	if !entry.session.ExpiresAt.After(r.now()) {
		r.removeLocked(id, ErrExpired)
		r.mu.Unlock()
		return Session{}, ErrExpired
	}
	if entry.session.Revision != expectedRevision {
		r.mu.Unlock()
		return Session{}, ErrRevisionConflict
	}
	binding, err := entry.session.Binding.withTarget(target)
	if err != nil {
		r.mu.Unlock()
		return Session{}, err
	}
	entry.session.Binding = binding
	snapshot := cloneSession(entry.session)
	r.mu.Unlock()
	return snapshot, nil
}

// Touch refreshes expiry without changing state revision.
func (r *Runtime) Touch(ctx context.Context, id string, ttl time.Duration) (Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	normalizedTTL, err := r.normalizeTTL(ttl)
	if err != nil {
		return Session{}, err
	}
	resolved, err := r.currentSessionMetadata(id)
	if err != nil {
		return Session{}, err
	}

	r.mu.Lock()
	entry, ok := r.sessions[id]
	if !ok || entry.session.Scope != resolved.Session.Scope {
		r.mu.Unlock()
		return Session{}, ErrNotFound
	}
	now := r.now()
	if !entry.session.ExpiresAt.After(now) {
		r.removeLocked(id, ErrExpired)
		r.mu.Unlock()
		return Session{}, ErrExpired
	}
	entry.session.ExpiresAt = now.Add(normalizedTTL)
	r.scheduleExpiryLocked(entry, entry.session.ExpiresAt)
	snapshot := cloneSession(entry.session)
	r.mu.Unlock()
	return snapshot, nil
}

// Cancel removes one session and cancels its context.
func (r *Runtime) Cancel(id string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.removeLocked(id, ErrCanceled)
}

// CancelScope removes all sessions owned by one plugin generation.
func (r *Runtime) CancelScope(scope tasks.ScopeIdentity) int {
	if r == nil || scope.IsZero() {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.byScope[scope]
	if len(ids) == 0 {
		return 0
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	removed := 0
	for _, id := range list {
		if r.removeLocked(id, ErrScopeStale) {
			removed++
		}
	}
	return removed
}

// PruneExpired reclaims all sessions whose TTL has elapsed.
func (r *Runtime) PruneExpired() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pruneExpiredLocked(r.now())
}

// Stats returns bounded-retention diagnostics and lazily removes expired entries.
func (r *Runtime) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.Lock()
	r.pruneExpiredLocked(r.now())
	stats := Stats{
		Sessions:         len(r.sessions),
		StateBytes:       r.stateBytes,
		Expired:          r.expiredCount,
		Canceled:         r.canceledCount,
		Stale:            r.staleCount,
		CapacityRejected: r.capacityRejectedCount,
	}
	r.mu.Unlock()
	return stats
}
