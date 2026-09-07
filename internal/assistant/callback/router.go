package callback

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/assistant/interaction"
	"go.uber.org/zap"
)

var (
	// ErrUnknownAction occurs when no handler matches the requested namespace and action.
	ErrUnknownAction = errors.New("assistant/callback: no handler registered for action")
)

// ActionHandler defines the signature for a callback action handler function.
type ActionHandler func(ctx context.Context, tx *Transaction) error

// Router dispatches incoming callback transactions to registered handlers with validation and authorization.
type Router struct {
	mu         sync.RWMutex
	handlers   map[string]ActionHandler
	authorizer Authorizer
	logger     *zap.Logger
}

// NewRouter creates an initialized callback Router.
func NewRouter(logger *zap.Logger) *Router {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Router{
		handlers:   make(map[string]ActionHandler),
		authorizer: AllowAllAuthorizer{},
		logger:     logger,
	}
}

// SetAuthorizer assigns an Authorizer to enforce access control on incoming callbacks.
func (r *Router) SetAuthorizer(auth Authorizer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if auth == nil {
		auth = AllowAllAuthorizer{}
	}
	r.authorizer = auth
}

// Register attaches a handler to a specific namespace and action.
func (r *Router) Register(namespace, action string, handler ActionHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := fmt.Sprintf("%s:%s", namespace, action)
	r.handlers[key] = handler
}

// Dispatch processes a CallbackTransaction through authorization, matching, and execution.
func (r *Router) Dispatch(ctx context.Context, tx *Transaction) error {
	if tx == nil {
		return interaction.ErrInvalidTarget
	}

	start := time.Now()
	correlationKey := fmt.Sprintf("%s:%s", tx.Payload.Namespace, tx.Payload.Action)

	// 1. Authorization check
	r.mu.RLock()
	authorizer := r.authorizer
	r.mu.RUnlock()

	if err := authorizer.Authorize(ctx, tx.UserID, tx.Payload.Action); err != nil {
		_ = tx.Answer(ctx, "⚠️ This is OWNER's bot!!", true)
		r.logger.Warn("assistant: unauthorized callback rejected",
			zap.Int64("user_id", tx.UserID),
			zap.String("action", tx.Payload.Action),
			zap.Int64("query_id", tx.QueryID),
		)
		tx.SetState(StateFailed)
		return interaction.ErrUnauthorized
	}

	// 2. Resolve handler
	r.mu.RLock()
	handler, ok := r.handlers[correlationKey]
	if !ok {
		// Try wildcard namespace handler
		handler, ok = r.handlers[fmt.Sprintf("%s:*", tx.Payload.Namespace)]
	}
	r.mu.RUnlock()

	if !ok {
		_ = tx.Answer(ctx, "Unknown button action", false)
		r.logger.Warn("assistant: unknown callback action",
			zap.String("key", correlationKey),
			zap.Int64("query_id", tx.QueryID),
		)
		tx.SetState(StateFailed)
		return fmt.Errorf("%w: %s", ErrUnknownAction, correlationKey)
	}

	// 3. Execute handler
	tx.SetState(StateExecuting)
	err := handler(ctx, tx)

	duration := time.Since(start)
	if err != nil {
		tx.SetState(StateFailed)
		r.logger.Error("assistant: callback handler failed",
			zap.String("key", correlationKey),
			zap.Int64("query_id", tx.QueryID),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
		return err
	}

	tx.SetState(StateCompleted)
	r.logger.Debug("assistant: callback handler completed",
		zap.String("key", correlationKey),
		zap.Int64("query_id", tx.QueryID),
		zap.Duration("duration", duration),
	)
	return nil
}
