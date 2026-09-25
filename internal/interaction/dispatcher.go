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
	Token       CallbackToken
	Session     Session
	Context     context.Context
	Preparation any
}

type ActionHandler func(context.Context, Action) error

// AckPolicy controls who owns the one Telegram callback-query acknowledgement.
// Handler-owned preserves rich toast/alert responses; immediate acknowledgement
// is reserved for actions whose post-admission feedback is rendered in-message.
type AckPolicy uint8

const (
	AckHandlerOwned AckPolicy = iota
	AckImmediate
)

// ActionAdmission is prepared before TaskEngine submission. FeatureScope remains
// owned by the a2 handler registration, while Scope may point at a dynamic
// downstream provider whose lifecycle/resource authority must govern execution.
type ActionAdmission struct {
	Scope     tasks.ScopeIdentity
	Profile   tasks.ExecutionProfile
	State     any
	AckPolicy AckPolicy
}

type ActionPreparer func(context.Context, Action) (ActionAdmission, error)

type handlerKey struct {
	feature string
	action  string
}

type handlerEntry struct {
	scope    tasks.ScopeIdentity
	handler  ActionHandler
	preparer ActionPreparer
	token    uint64
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

// PreparedAction is an opaque, generation-bound action lease. It is prepared
// before TaskEngine admission and revalidated immediately before execution.
type PreparedAction interface {
	Scope() tasks.ScopeIdentity
	Dispatch(context.Context) error
}

// ResourcePreparedAction is an optional prepared-action capability. Existing
// consumers that only depend on Scope+Dispatch remain source-compatible.
type ResourcePreparedAction interface {
	PreparedAction
	Resources() []tasks.ResourceRequirement
}

// ExecutionProfilePreparedAction exposes the complete TaskEngine admission
// profile chosen during action preparation.
type ExecutionProfilePreparedAction interface {
	PreparedAction
	ExecutionProfile() tasks.ExecutionProfile
}

// AckPreparedAction exposes callback acknowledgement ownership determined
// during side-effect-free action preparation.
type AckPreparedAction interface {
	PreparedAction
	AckPolicy() AckPolicy
}

type preparedAction struct {
	dispatcher     *Dispatcher
	data           []byte
	binding        Binding
	key            handlerKey
	scope          tasks.ScopeIdentity
	executionScope tasks.ScopeIdentity
	profile        tasks.ExecutionProfile
	preparation    any
	ackPolicy      AckPolicy
	handlerToken   uint64
}

func (p *preparedAction) Scope() tasks.ScopeIdentity {
	if p == nil {
		return tasks.ScopeIdentity{}
	}
	if !p.executionScope.IsZero() {
		return p.executionScope
	}
	return p.scope
}

func (p *preparedAction) Resources() []tasks.ResourceRequirement {
	if p == nil {
		return nil
	}
	return append([]tasks.ResourceRequirement(nil), p.profile.Resources...)
}

func (p *preparedAction) ExecutionProfile() tasks.ExecutionProfile {
	if p == nil {
		return tasks.ExecutionProfile{}
	}
	return p.profile.WithDefaults(tasks.ExecutionProfile{})
}

func (p *preparedAction) AckPolicy() AckPolicy {
	if p == nil {
		return AckHandlerOwned
	}
	return p.ackPolicy
}

func (p *preparedAction) Dispatch(ctx context.Context) error {
	if p == nil || p.dispatcher == nil {
		return ErrHandlerUnavailable
	}
	return p.dispatcher.dispatchPrepared(ctx, p)
}

func NewDispatcher(sessions *Runtime) *Dispatcher {
	return &Dispatcher{sessions: sessions, handlers: make(map[handlerKey]handlerEntry)}
}

// Runtime returns the P1 session runtime owned by this dispatcher.
func (d *Dispatcher) Runtime() *Runtime {
	if d == nil {
		return nil
	}
	return d.sessions
}

func (d *Dispatcher) Register(scope tasks.ScopeIdentity, featureID, actionID string, handler ActionHandler) (*HandlerRegistration, error) {
	return d.register(scope, featureID, actionID, nil, handler)
}

// RegisterPrepared adds a side-effect-free admission preparer. It is intended
// for actions whose real execution owner/resources are resolved from bounded
// session state (for example a SavedResponse provider binding).
func (d *Dispatcher) RegisterPrepared(
	scope tasks.ScopeIdentity,
	featureID, actionID string,
	preparer ActionPreparer,
	handler ActionHandler,
) (*HandlerRegistration, error) {
	if preparer == nil {
		return nil, ErrHandlerUnavailable
	}
	return d.register(scope, featureID, actionID, preparer, handler)
}

func (d *Dispatcher) register(
	scope tasks.ScopeIdentity,
	featureID, actionID string,
	preparer ActionPreparer,
	handler ActionHandler,
) (*HandlerRegistration, error) {
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
	d.handlers[key] = handlerEntry{scope: scope, handler: handler, preparer: preparer, token: token}
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

// UnregisterScope removes handlers owned by exactly one plugin generation.
// Newer replacement generations remain intact.
func (d *Dispatcher) UnregisterScope(scope tasks.ScopeIdentity) int {
	if d == nil || scope.IsZero() {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	removed := 0
	for key, entry := range d.handlers {
		if entry.scope == scope {
			delete(d.handlers, key)
			removed++
		}
	}
	return removed
}

// Prepare validates the callback and pins the exact handler registration and
// plugin generation without invoking feature code. The returned lease can be
// admitted to TaskEngine using Scope(), then Dispatch revalidates everything
// that may have changed while the task was queued.
func (d *Dispatcher) Prepare(ctx context.Context, data []byte, binding Binding) (PreparedAction, error) {
	if d == nil || d.sessions == nil {
		return nil, ErrHandlerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := d.sessions.ResolveCallback(ctx, data, binding)
	if err != nil {
		return nil, err
	}
	key := handlerKey{feature: resolved.Token.FeatureID, action: resolved.Token.ActionID}
	d.mu.RLock()
	entry, ok := d.handlers[key]
	d.mu.RUnlock()
	if !ok || entry.scope != resolved.Session.Scope {
		return nil, ErrHandlerUnavailable
	}
	current, available := d.sessions.catalog.FeatureScope(resolved.Session.FeatureID)
	if !available || current != entry.scope {
		return nil, ErrScopeStale
	}
	admission := ActionAdmission{Scope: entry.scope}
	if entry.preparer != nil {
		admission, err = entry.preparer(ctx, Action{
			Token:   resolved.Token,
			Session: resolved.Session,
			Context: resolved.Context,
		})
		if err != nil {
			return nil, err
		}
		if admission.Scope.IsZero() {
			return nil, ErrHandlerUnavailable
		}
	}
	current, available = d.sessions.catalog.FeatureScope(resolved.Session.FeatureID)
	if !available || current != entry.scope {
		return nil, ErrScopeStale
	}
	return &preparedAction{
		dispatcher:     d,
		data:           append([]byte(nil), data...),
		binding:        binding.normalized(),
		key:            key,
		scope:          entry.scope,
		executionScope: admission.Scope,
		profile:        admission.Profile.WithDefaults(tasks.ExecutionProfile{}),
		preparation:    admission.State,
		ackPolicy:      admission.AckPolicy,
		handlerToken:   entry.token,
	}, nil
}

func (d *Dispatcher) dispatchPrepared(ctx context.Context, prepared *preparedAction) error {
	if d == nil || d.sessions == nil || prepared == nil || prepared.dispatcher != d {
		return ErrHandlerUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Re-resolve the token after queueing so a state transition, expiry,
	// disable/reload, or target change cannot execute a lease prepared against
	// older interaction state.
	resolved, err := d.sessions.ResolveCallback(ctx, prepared.data, prepared.binding)
	if err != nil {
		return err
	}
	key := handlerKey{feature: resolved.Token.FeatureID, action: resolved.Token.ActionID}
	if key != prepared.key || resolved.Session.Scope != prepared.scope {
		return ErrScopeStale
	}

	d.mu.RLock()
	entry, ok := d.handlers[key]
	d.mu.RUnlock()
	if !ok || entry.scope != prepared.scope || entry.token != prepared.handlerToken {
		return ErrHandlerUnavailable
	}
	current, available := d.sessions.catalog.FeatureScope(resolved.Session.FeatureID)
	if !available || current != prepared.scope {
		return ErrScopeStale
	}

	actionCtx, release := mergeActionContext(ctx, resolved.Context)
	defer release()
	return entry.handler(actionCtx, Action{
		Token:       resolved.Token,
		Session:     resolved.Session,
		Context:     actionCtx,
		Preparation: prepared.preparation,
	})
}

func (d *Dispatcher) Dispatch(ctx context.Context, data []byte, binding Binding) error {
	prepared, err := d.Prepare(ctx, data, binding)
	if err != nil {
		return err
	}
	return prepared.Dispatch(ctx)
}

func mergeActionContext(caller, session context.Context) (context.Context, func()) {
	if caller == nil {
		caller = context.Background()
	}
	ctx, cancel := context.WithCancelCause(caller)
	if session == nil {
		return ctx, func() { cancel(context.Canceled) }
	}
	if err := session.Err(); err != nil {
		cancel(context.Cause(session))
		return ctx, func() { cancel(context.Canceled) }
	}
	stop := context.AfterFunc(session, func() {
		cancel(context.Cause(session))
	})
	return ctx, func() {
		stop()
		cancel(context.Canceled)
	}
}
