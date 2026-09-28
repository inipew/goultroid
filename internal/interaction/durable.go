package interaction

import (
	"context"
	"time"
)

// DurableSession is the persisted representation of an opted-in A2 session.
type DurableSession struct {
	Session      Session
	Version      string
	InputExpires time.Time
}

// DurableStore stores sessions for a single active bot process.
type DurableStore interface {
	Save(context.Context, DurableSession) error
	Delete(context.Context, string) error
	Load(context.Context) ([]DurableSession, error)
}

type durabilityCatalog interface{ DurabilityVersion(string) string }

func (r *Runtime) durabilityVersion(featureID string) string {
	if catalog, ok := r.catalog.(durabilityCatalog); ok {
		return catalog.DurabilityVersion(featureID)
	}
	return ""
}

// SetDurableStore attaches persistence before sessions are created.
func (r *Runtime) SetDurableStore(store DurableStore) error {
	if r == nil || store == nil {
		return ErrInvalidConfig
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.durable != nil || len(r.sessions) != 0 {
		return ErrInvalidConfig
	}
	r.durable = store
	return nil
}

// PreserveDurableOnShutdown keeps stored rows while plugin teardown clears memory.
func (r *Runtime) PreserveDurableOnShutdown() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.preserveDurable = true
	r.mu.Unlock()
}

func (r *Runtime) saveDurableLocked(ctx context.Context, session Session, input *inputClaim) error {
	if r.durable == nil {
		return nil
	}
	version := r.durabilityVersion(session.FeatureID)
	if version == "" {
		return nil
	}
	row := DurableSession{Session: cloneSession(session), Version: version}
	if input != nil {
		row.InputExpires = input.expiresAt
	}
	if err := r.durable.Save(ctx, row); err != nil {
		r.persistenceErrorCount++
		return err
	}
	return nil
}

// RestoreDurable reloads compatible live sessions after plugin registration.
func (r *Runtime) RestoreDurable(ctx context.Context) error {
	if r == nil {
		return ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if r.durable == nil {
		return nil
	}
	rows, err := r.durable.Load(ctx)
	if err != nil {
		r.persistenceErrorCount++
		return err
	}
	now := r.now()
	for _, row := range rows {
		s := row.Session
		scope, ok := r.catalog.FeatureScope(s.FeatureID)
		if !ok || row.Version == "" || row.Version != r.durabilityVersion(s.FeatureID) || !s.ExpiresAt.After(now) || !validSessionID(s.ID) || s.Revision == 0 || s.Binding.Validate() != nil || len(s.State) > r.config.MaxStateBytes {
			if err := r.durable.Delete(ctx, s.ID); err != nil {
				r.persistenceErrorCount++
				return err
			}
			r.restoreRejectedCount++
			continue
		}
		if _, exists := r.sessions[s.ID]; exists {
			continue
		}
		if err := r.checkCapacityLocked(scope, s.Binding.ActorID, len(s.State)); err != nil {
			if err := r.durable.Delete(ctx, s.ID); err != nil {
				r.persistenceErrorCount++
				return err
			}
			r.restoreRejectedCount++
			continue
		}
		s.Scope = scope
		sessionCtx, cancel := context.WithCancelCause(r.rootCtx)
		entry := &sessionEntry{session: cloneSession(s), durableVersion: row.Version, ctx: sessionCtx, cancel: cancel}
		if !row.InputExpires.IsZero() && row.InputExpires.After(now) {
			key, keyErr := inputKeyFromBinding(s.Binding)
			if keyErr != nil {
				cancel(keyErr)
				if err := r.durable.Delete(ctx, s.ID); err != nil {
					r.persistenceErrorCount++
					return err
				}
				r.restoreRejectedCount++
				continue
			}
			if _, busy := r.inputs[key]; busy {
				cancel(ErrInputBusy)
				if err := r.durable.Delete(ctx, s.ID); err != nil {
					r.persistenceErrorCount++
					return err
				}
				r.restoreRejectedCount++
				continue
			}
			entry.input = &inputClaim{key: key, expiresAt: row.InputExpires}
			r.inputs[key] = s.ID
		}
		r.sessions[s.ID] = entry
		r.indexSessionLocked(entry)
		r.scheduleExpiryLocked(entry, s.ExpiresAt)
		r.restoredCount++
	}
	return nil
}
