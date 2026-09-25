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
	// Inline results are created before Telegram assigns a concrete inline
	// message target, so those sessions initially bind only the actor. The first
	// callback atomically claims its concrete target; later callbacks from a
	// copied/different message then fail the ordinary binding check.
	if !bindingHasTarget(resolved.Session.Binding) {
		if target, ok := completeTargetFromBinding(actual); ok {
			bound, bindErr := r.BindTarget(ctx, token.SessionID, token.Revision, target)
			if bindErr != nil {
				return ResolvedCallback{}, bindErr
			}
			resolved.Session = bound
		}
	}
	token.FeatureID = resolved.Session.FeatureID
	if resolved.Session.Revision != token.Revision {
		return ResolvedCallback{}, ErrStaleToken
	}
	if !r.catalog.HasAction(resolved.Session.FeatureID, token.ActionID) {
		return ResolvedCallback{}, ErrActionNotFound
	}
	return ResolvedCallback{Token: token, Session: resolved.Session, Context: resolved.Context}, nil
}

func bindingHasTarget(binding Binding) bool {
	return binding.ChatID != 0 || binding.MessageID != 0 || binding.InlineMessageID != ""
}

func completeTargetFromBinding(binding Binding) (TargetBinding, bool) {
	if binding.InlineMessageID != "" && binding.ChatID == 0 && binding.MessageID == 0 {
		return TargetBinding{InlineMessageID: binding.InlineMessageID}, true
	}
	if binding.ChatID != 0 && binding.MessageID > 0 && binding.InlineMessageID == "" {
		return TargetBinding{ChatID: binding.ChatID, MessageID: binding.MessageID}, true
	}
	return TargetBinding{}, false
}
