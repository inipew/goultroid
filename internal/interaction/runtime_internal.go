package interaction

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func (r *Runtime) currentSession(id string) (Resolved, error) {
	return r.loadCurrentSession(id, true)
}

func (r *Runtime) currentSessionMetadata(id string) (Resolved, error) {
	return r.loadCurrentSession(id, false)
}

func (r *Runtime) currentSessionMetadataContext(ctx context.Context, id string) (Resolved, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}
	return r.currentSessionMetadata(id)
}

func (r *Runtime) loadCurrentSession(id string, includeState bool) (Resolved, error) {
	if r == nil || !validSessionID(id) {
		return Resolved{}, ErrNotFound
	}
	now := r.now()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return Resolved{}, ErrClosed
	}
	entry, ok := r.sessions[id]
	if !ok {
		r.pruneExpiredLocked(now)
		r.mu.Unlock()
		return Resolved{}, ErrNotFound
	}
	if !entry.session.ExpiresAt.After(now) {
		r.removeLocked(id, ErrExpired)
		r.mu.Unlock()
		return Resolved{}, ErrExpired
	}
	snapshot := entry.session
	if includeState {
		snapshot.State = append([]byte(nil), entry.session.State...)
	} else {
		snapshot.State = nil
	}
	sessionCtx := entry.ctx
	r.mu.Unlock()

	currentScope, ok := r.catalog.FeatureScope(snapshot.FeatureID)
	if !ok || currentScope != snapshot.Scope {
		r.cancelIfScope(id, snapshot.Scope, ErrScopeStale)
		return Resolved{}, ErrScopeStale
	}
	return Resolved{Session: snapshot, Context: sessionCtx}, nil
}

func (r *Runtime) cancelIfScope(id string, scope tasks.ScopeIdentity, cause error) {
	r.mu.Lock()
	entry, ok := r.sessions[id]
	if ok && entry.session.Scope == scope {
		r.removeLocked(id, cause)
	}
	r.mu.Unlock()
}

func (r *Runtime) normalizeTTL(ttl time.Duration) (time.Duration, error) {
	if ttl < 0 {
		return 0, ErrInvalidTTL
	}
	if ttl == 0 {
		ttl = r.config.DefaultTTL
	}
	if ttl > r.config.MaxTTL {
		return 0, ErrInvalidTTL
	}
	return ttl, nil
}

func (r *Runtime) checkCapacityLocked(scope tasks.ScopeIdentity, actorID int64, stateBytes int) error {
	if len(r.sessions) >= r.config.MaxSessions {
		return ErrCapacity
	}
	if len(r.byScope[scope]) >= r.config.MaxSessionsPerScope {
		return ErrCapacity
	}
	if actorID != 0 && r.actorCounts[actorID] >= r.config.MaxSessionsPerActor {
		return ErrCapacity
	}
	if r.stateBytes+stateBytes > r.config.MaxTotalStateBytes {
		return ErrCapacity
	}
	return nil
}

func (r *Runtime) uniqueIDLocked() (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		id, err := r.newID()
		if err != nil {
			return "", err
		}
		if !validSessionID(id) {
			return "", fmt.Errorf("generate interaction session id: invalid generator output")
		}
		if _, exists := r.sessions[id]; !exists {
			return id, nil
		}
	}
	return "", errors.New("interaction: repeated session id collision")
}

func (r *Runtime) indexSessionLocked(entry *sessionEntry) {
	scope := entry.session.Scope
	ids := r.byScope[scope]
	if ids == nil {
		ids = make(map[string]struct{})
		r.byScope[scope] = ids
	}
	ids[entry.session.ID] = struct{}{}
	if actorID := entry.session.Binding.ActorID; actorID != 0 {
		r.actorCounts[actorID]++
	}
	r.stateBytes += len(entry.session.State)
}

func (r *Runtime) scheduleExpiryLocked(entry *sessionEntry, expiresAt time.Time) {
	if entry.expiry == nil {
		entry.expiry = &expiryItem{id: entry.session.ID, expiresAt: expiresAt, index: -1}
		heap.Push(&r.expiries, entry.expiry)
		return
	}
	entry.expiry.expiresAt = expiresAt
	heap.Fix(&r.expiries, entry.expiry.index)
}

func (r *Runtime) pruneExpiredLocked(now time.Time) int {
	removed := 0
	for r.expiries.Len() > 0 {
		item := r.expiries[0]
		if item.expiresAt.After(now) {
			break
		}
		heap.Pop(&r.expiries)
		entry, ok := r.sessions[item.id]
		if !ok || entry.expiry != item {
			continue
		}
		entry.expiry = nil
		if r.removeLocked(item.id, ErrExpired) {
			removed++
		}
	}
	return removed
}

func (r *Runtime) removeLocked(id string, cause error) bool {
	entry, ok := r.sessions[id]
	if !ok {
		return false
	}
	delete(r.sessions, id)
	if entry.expiry != nil && entry.expiry.index >= 0 {
		heap.Remove(&r.expiries, entry.expiry.index)
		entry.expiry = nil
	}
	ids := r.byScope[entry.session.Scope]
	delete(ids, id)
	if len(ids) == 0 {
		delete(r.byScope, entry.session.Scope)
	}
	if actorID := entry.session.Binding.ActorID; actorID != 0 {
		if r.actorCounts[actorID] <= 1 {
			delete(r.actorCounts, actorID)
		} else {
			r.actorCounts[actorID]--
		}
	}
	r.stateBytes -= len(entry.session.State)
	entry.cancel(cause)
	switch {
	case errors.Is(cause, ErrExpired):
		r.expiredCount++
	case errors.Is(cause, ErrScopeStale):
		r.staleCount++
	default:
		r.canceledCount++
	}
	return true
}

func cloneSession(session Session) Session {
	session.State = append([]byte(nil), session.State...)
	return session
}
