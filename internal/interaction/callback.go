package interaction

import "context"

// CallbackData builds callback data for the session's current state revision.
func (r *Runtime) CallbackData(ctx context.Context, sessionID, actionID string) ([]byte, error) {
	resolved, err := r.currentSessionMetadataContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	actionID = normalizeFeatureID(actionID)
	if !validIdentifier(actionID) || !r.catalog.HasAction(resolved.Session.FeatureID, actionID) {
		return nil, ErrActionNotFound
	}
	return EncodeCallbackToken(resolved.Session.FeatureID, actionID, resolved.Session.ID, resolved.Session.Revision)
}

// ResolveCallback validates protocol version, session generation, revision,
// actor/target binding, and declared P0 action identity.
func (r *Runtime) ResolveCallback(ctx context.Context, data []byte, actual Binding) (ResolvedCallback, error) {
	token, err := ParseCallbackToken(data)
	if err != nil {
		return ResolvedCallback{}, err
	}
	resolved, err := r.Resolve(ctx, token.SessionID, actual)
	if err != nil {
		return ResolvedCallback{}, err
	}
	if resolved.Session.FeatureID != token.FeatureID {
		return ResolvedCallback{}, ErrTokenMismatch
	}
	if resolved.Session.Revision != token.Revision {
		return ResolvedCallback{}, ErrStaleToken
	}
	if !r.catalog.HasAction(token.FeatureID, token.ActionID) {
		return ResolvedCallback{}, ErrActionNotFound
	}
	return ResolvedCallback{Token: token, Session: resolved.Session, Context: resolved.Context}, nil
}
