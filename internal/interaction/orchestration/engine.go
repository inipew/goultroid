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

// Dispatch derives the P1 binding from the concrete callback target and actor,
// preventing a caller from validating one target while editing another.
func (e *Engine) Dispatch(ctx context.Context, request CallbackRequest) error {
	if e == nil {
		return ErrInvalidEngine
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, binding, err := sessionTarget(request.Target, request.ActorID)
	if err != nil {
		return err
	}
	if request.QueryID == 0 {
		return fmt.Errorf("%w: query id is zero", ErrNoCallback)
	}
	ctx = withCallbackInvocation(ctx, callbackInvocation{queryID: request.QueryID, target: request.Target})
	return e.actions.Dispatch(ctx, request.Data, binding)
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
