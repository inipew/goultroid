package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/tasks"
)

var (
	ErrInvalidEngine = errors.New("interaction/orchestration: invalid engine")
	ErrInvalidTarget = errors.New("interaction/orchestration: target cannot provide interaction binding")
	ErrNoCallback    = errors.New("interaction/orchestration: callback query is unavailable")
)

type Engine struct {
	sessions *interaction.Runtime
	actions  *interaction.Dispatcher
	compiler *presentation.Compiler
	port     presentation.Port
}

type BeginRequest struct {
	FeatureID string
	ActorID   int64
	State     []byte
	TTL       time.Duration
	Target    presentation.Target
	View      presentation.View
}

type CallbackRequest struct {
	Data    []byte
	ActorID int64
	QueryID int64
	Target  presentation.Target
}

// PreparedCallback is an opaque execution lease for one validated a2 action.
// The transport uses Scope for TaskEngine admission and Dispatch for the
// revalidated feature invocation.
type PreparedCallback interface {
	Scope() tasks.ScopeIdentity
	Dispatch(context.Context) error
}

// ResourcePreparedCallback is an optional admission capability for prepared
// callbacks that need global resource budgeting.
type ResourcePreparedCallback interface {
	PreparedCallback
	Resources() []tasks.ResourceRequirement
}

// AckPreparedCallback exposes whether transport may acknowledge immediately
// after TaskEngine admission or must leave the callback answer to the handler.
type AckPreparedCallback interface {
	PreparedCallback
	AckPolicy() interaction.AckPolicy
}

type preparedCallback struct {
	action  interaction.PreparedAction
	target  presentation.Target
	queryID int64
}

func (p *preparedCallback) Scope() tasks.ScopeIdentity {
	if p == nil || p.action == nil {
		return tasks.ScopeIdentity{}
	}
	return p.action.Scope()
}

func (p *preparedCallback) Resources() []tasks.ResourceRequirement {
	if p == nil || p.action == nil {
		return nil
	}
	if aware, ok := p.action.(interaction.ResourcePreparedAction); ok {
		return aware.Resources()
	}
	return nil
}

func (p *preparedCallback) AckPolicy() interaction.AckPolicy {
	if p == nil || p.action == nil {
		return interaction.AckHandlerOwned
	}
	if aware, ok := p.action.(interaction.AckPreparedAction); ok {
		return aware.AckPolicy()
	}
	return interaction.AckHandlerOwned
}

func (p *preparedCallback) Dispatch(ctx context.Context) error {
	if p == nil || p.action == nil {
		return ErrInvalidEngine
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = withCallbackInvocation(ctx, callbackInvocation{queryID: p.queryID, target: p.target})
	return p.action.Dispatch(ctx)
}

type Handler func(*Context) error

func New(sessions *interaction.Runtime, actions *interaction.Dispatcher, port presentation.Port) (*Engine, error) {
	if sessions == nil || actions == nil || actions.Runtime() != sessions || port == nil {
		return nil, ErrInvalidEngine
	}
	return &Engine{
		sessions: sessions,
		actions:  actions,
		compiler: presentation.NewCompiler(sessions),
		port:     port,
	}, nil
}

// Begin atomically owns orchestration of session creation, callback compilation,
// transport send, and concrete target binding. Any failure after session creation
// cancels the session so a partially rendered view fails closed.
func (e *Engine) Begin(ctx context.Context, request BeginRequest) (*Context, error) {
	if e == nil {
		return nil, ErrInvalidEngine
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target, binding, err := sessionTarget(request.Target, request.ActorID)
	if err != nil {
		return nil, err
	}
	resolved, err := e.sessions.Create(ctx, interaction.CreateRequest{
		FeatureID: request.FeatureID,
		Binding:   binding,
		State:     request.State,
		TTL:       request.TTL,
	})
	if err != nil {
		return nil, err
	}
	sessionID := resolved.Session.ID
	committed := false
	defer func() {
		if !committed {
			e.sessions.Cancel(sessionID)
		}
	}()

	compiled, err := e.compiler.Compile(ctx, sessionID, request.View)
	if err != nil {
		return nil, err
	}
	sentTarget, err := e.port.Send(ctx, target, compiled)
	if err != nil {
		return nil, err
	}
	boundTarget, ok := sentTarget.(presentation.SessionTarget)
	if !ok {
		return nil, ErrInvalidTarget
	}
	concrete, ok := boundTarget.TargetBinding()
	if !ok {
		return nil, ErrInvalidTarget
	}
	bound, err := e.sessions.BindTarget(ctx, sessionID, resolved.Session.Revision, concrete)
	if err != nil {
		return nil, err
	}
	committed = true
	return newContext(resolved.Context, e, bound, sentTarget, 0), nil
}

// RegisterAction exposes the typed feature-facing callback handler contract while
// retaining the exact P2 scope-generation registration semantics.
func (e *Engine) RegisterAction(scope tasks.ScopeIdentity, featureID, actionID string, handler Handler) (*interaction.HandlerRegistration, error) {
	if e == nil || handler == nil {
		return nil, interaction.ErrHandlerUnavailable
	}
	return e.actions.Register(scope, featureID, actionID, func(ctx context.Context, action interaction.Action) error {
		invocation, ok := callbackInvocationFromContext(ctx)
		if !ok {
			return ErrNoCallback
		}
		return handler(newContext(ctx, e, action.Session, invocation.target, invocation.queryID))
	})
}

// RegisterPreparedAction lets a typed a2 action resolve a dynamic downstream
// execution owner/resources before TaskEngine admission while keeping callback
// identity/session ownership on the declaring feature.
func (e *Engine) RegisterPreparedAction(
	scope tasks.ScopeIdentity,
	featureID, actionID string,
	preparer interaction.ActionPreparer,
	handler Handler,
) (*interaction.HandlerRegistration, error) {
	if e == nil || preparer == nil || handler == nil {
		return nil, interaction.ErrHandlerUnavailable
	}
	return e.actions.RegisterPrepared(scope, featureID, actionID, preparer, func(ctx context.Context, action interaction.Action) error {
		invocation, ok := callbackInvocationFromContext(ctx)
		if !ok {
			return ErrNoCallback
		}
		actionCtx := newContext(ctx, e, action.Session, invocation.target, invocation.queryID)
		actionCtx.preparation = action.Preparation
		return handler(actionCtx)
	})
}

// TakeInput atomically consumes one pending actor+chat input claim and returns
// a feature-facing context with the session revision already advanced. The
// transport attaches the concrete original presentation target before invoking
// the feature handler.
func (e *Engine) TakeInput(ctx context.Context, actorID, chatID int64) (*Context, bool, error) {
	if e == nil {
		return nil, false, ErrInvalidEngine
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, handled, err := e.sessions.TakeInput(ctx, actorID, chatID)
	if err != nil || !handled {
		return nil, handled, err
	}
	return newContext(resolved.Context, e, resolved.Session, nil, 0), true, nil
}

// PrepareCallback derives the P1 binding from the concrete callback target,
// validates the current session/action generation, and returns a lease suitable
// for TaskEngine admission. No feature handler is invoked during preparation.
func (e *Engine) PrepareCallback(ctx context.Context, request CallbackRequest) (PreparedCallback, error) {
	if e == nil {
		return nil, ErrInvalidEngine
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, binding, err := sessionTarget(request.Target, request.ActorID)
	if err != nil {
		return nil, err
	}
	if request.QueryID == 0 {
		return nil, fmt.Errorf("%w: query id is zero", ErrNoCallback)
	}
	action, err := e.actions.Prepare(ctx, request.Data, binding)
	if err != nil {
		return nil, err
	}
	return &preparedCallback{action: action, target: request.Target, queryID: request.QueryID}, nil
}

// Dispatch remains the synchronous feature-facing convenience path. Live
// Assistant ingress uses PrepareCallback -> TaskEngine -> PreparedCallback.Dispatch.
func (e *Engine) Dispatch(ctx context.Context, request CallbackRequest) error {
	prepared, err := e.PrepareCallback(ctx, request)
	if err != nil {
		return err
	}
	return prepared.Dispatch(ctx)
}

func sessionTarget(target presentation.Target, actorID int64) (presentation.SessionTarget, interaction.Binding, error) {
	bound, ok := target.(presentation.SessionTarget)
	if !ok || bound == nil {
		return nil, interaction.Binding{}, ErrInvalidTarget
	}
	binding := bound.SessionBinding(actorID)
	if err := binding.Validate(); err != nil {
		return nil, interaction.Binding{}, err
	}
	return bound, binding, nil
}
