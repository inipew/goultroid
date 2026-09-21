package interaction

import (
	"context"
	"time"
)

type inputBindingKey struct {
	actorID int64
	chatID  int64
}

type inputClaim struct {
	key       inputBindingKey
	expiresAt time.Time
}

// ArmInput atomically advances the session revision/state and installs one
// actor+chat-bound pending input claim. A live claim owned by another session
// for the same actor/chat fails closed with ErrInputBusy.
func (r *Runtime) ArmInput(ctx context.Context, id string, request InputRequest) (Session, error) {
	if r == nil {
		return Session{}, ErrClosed
	}
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
	ttl, err := r.normalizeTTL(request.TTL)
	if err != nil {
		return Session{}, err
	}

	resolved, err := r.currentSessionMetadata(id)
	if err != nil {
		return Session{}, err
	}
	key, err := inputKeyFromBinding(resolved.Session.Binding)
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

	if existingID, exists := r.inputs[key]; exists && existingID != id {
		existing := r.sessions[existingID]
		if existing == nil || existing.input == nil || !existing.input.expiresAt.After(now) {
			if existing != nil {
				r.clearInputLocked(existing)
			} else {
				delete(r.inputs, key)
			}
		} else {
			r.mu.Unlock()
			return Session{}, ErrInputBusy
		}
	}

	newTotal := r.stateBytes - len(entry.session.State) + len(request.State)
	if newTotal > r.config.MaxTotalStateBytes {
		r.capacityRejectedCount++
		r.mu.Unlock()
		return Session{}, ErrCapacity
	}

	r.clearInputLocked(entry)
	r.stateBytes = newTotal
	entry.session.State = append([]byte(nil), request.State...)
	entry.session.Revision++

	expiresAt := now.Add(ttl)
	if expiresAt.After(entry.session.ExpiresAt) {
		expiresAt = entry.session.ExpiresAt
	}
	entry.input = &inputClaim{key: key, expiresAt: expiresAt}
	r.inputs[key] = id

	snapshot := cloneSession(entry.session)
	r.mu.Unlock()
	return snapshot, nil
}

// TakeInput atomically claims one pending input delivery for actor/chat and
// advances the session revision before returning it to the handler. This makes
// concurrent messages and previously rendered cancel buttons stale before any
// persistence work begins.
func (r *Runtime) TakeInput(ctx context.Context, actorID, chatID int64) (Resolved, bool, error) {
	if r == nil {
		return Resolved{}, false, ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Resolved{}, false, err
	}
	if actorID == 0 || chatID == 0 {
		return Resolved{}, false, ErrInvalidBinding
	}

	key := inputBindingKey{actorID: actorID, chatID: chatID}
	now := r.now()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return Resolved{}, false, ErrClosed
	}
	id, ok := r.inputs[key]
	if !ok {
		r.mu.Unlock()
		return Resolved{}, false, nil
	}
	entry, ok := r.sessions[id]
	if !ok || entry.input == nil || entry.input.key != key {
		delete(r.inputs, key)
		r.mu.Unlock()
		return Resolved{}, false, nil
	}
	if !entry.input.expiresAt.After(now) {
		r.clearInputLocked(entry)
		r.mu.Unlock()
		return Resolved{}, true, ErrInputExpired
	}
	if !entry.session.ExpiresAt.After(now) {
		r.removeLocked(id, ErrExpired)
		r.mu.Unlock()
		return Resolved{}, true, ErrExpired
	}

	r.clearInputLocked(entry)
	entry.session.Revision++
	snapshot := cloneSession(entry.session)
	sessionCtx := entry.ctx
	r.mu.Unlock()

	currentScope, ok := r.catalog.FeatureScope(snapshot.FeatureID)
	if !ok || currentScope != snapshot.Scope {
		r.cancelIfScope(snapshot.ID, snapshot.Scope, ErrScopeStale)
		return Resolved{}, true, ErrScopeStale
	}
	return Resolved{Session: snapshot, Context: sessionCtx}, true, nil
}

// ReleaseInput removes a pending input claim without canceling its interaction
// session. It is used when presentation failed after arming input.
func (r *Runtime) ReleaseInput(id string) bool {
	if r == nil || !validSessionID(id) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.sessions[id]
	if !ok {
		return false
	}
	return r.clearInputLocked(entry)
}
