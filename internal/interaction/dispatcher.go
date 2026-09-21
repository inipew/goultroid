package interaction

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrHandlerRegistered  = errors.New("interaction: action handler already registered")
	ErrHandlerUnavailable = errors.New("interaction: action handler unavailable")
)

type Action struct {
	Token   CallbackToken
	Session Session
	Context context.Context
}

type ActionHandler func(context.Context, Action) error

type handlerKey struct {
	feature string
	action  string
}

type handlerEntry struct {
	scope   tasks.ScopeIdentity
	handler ActionHandler
	token   uint64
}

type Dispatcher struct {
	mu       sync.RWMutex
	sessions *Runtime
	handlers map[handlerKey]handlerEntry
	next     uint64
}

type HandlerRegistration struct {
	dispatcher *Dispatcher
	key        handlerKey
	token      uint64
	once       sync.Once
}

func NewDispatcher(sessions *Runtime) *Dispatcher {
	return &Dispatcher{sessions: sessions, handlers: make(map[handlerKey]handlerEntry)}
}

func (d *Dispatcher) Register(scope tasks.ScopeIdentity, featureID, actionID string, handler ActionHandler) (*HandlerRegistration, error) {
	if d == nil || d.sessions == nil || scope.IsZero() || handler == nil {
		return nil, ErrHandlerUnavailable
	}
	featureID = normalizeFeatureID(featureID)
	actionID = normalizeFeatureID(actionID)
	if !validIdentifier(featureID) || !validIdentifier(actionID) || !d.sessions.catalog.HasAction(featureID, actionID) {
		return nil, ErrActionNotFound
	}
	currentScope, ok := d.sessions.catalog.FeatureScope(featureID)
	if !ok || currentScope != scope {
		return nil, ErrScopeStale
	}
	key := handlerKey{feature: featureID, action: actionID}
	d.mu.Lock()
	defer d.mu.Unlock()
	if current, exists := d.handlers[key]; exists && current.scope == scope {
		return nil, fmt.Errorf("%w: %s:%s", ErrHandlerRegistered, featureID, actionID)
	}
	d.next++
	token := d.next
	d.handlers[key] = handlerEntry{scope: scope, handler: handler, token: token}
	return &HandlerRegistration{dispatcher: d, key: key, token: token}, nil
}

func (r *HandlerRegistration) Close() {
	if r == nil || r.dispatcher == nil {
		return
	}
	r.once.Do(func() {
		r.dispatcher.mu.Lock()
		current, ok := r.dispatcher.handlers[r.key]
		if ok && current.token == r.token {
			delete(r.dispatcher.handlers, r.key)
		}
		r.dispatcher.mu.Unlock()
	})
}

func (d *Dispatcher) Dispatch(ctx context.Context, data []byte, binding Binding) error {
	if d == nil || d.sessions == nil {
		return ErrHandlerUnavailable
	}
	resolved, err := d.sessions.ResolveCallback(ctx, data, binding)
	if err != nil {
		return err
	}
	key := handlerKey{feature: resolved.Token.FeatureID, action: resolved.Token.ActionID}
	d.mu.RLock()
	entry, ok := d.handlers[key]
	d.mu.RUnlock()
	if !ok || entry.scope != resolved.Session.Scope {
		return ErrHandlerUnavailable
	}
	current, available := d.sessions.catalog.FeatureScope(resolved.Session.FeatureID)
	if !available || current != entry.scope {
		return ErrScopeStale
	}
	return entry.handler(resolved.Context, Action{
		Token:   resolved.Token,
		Session: resolved.Session,
		Context: resolved.Context,
	})
}
