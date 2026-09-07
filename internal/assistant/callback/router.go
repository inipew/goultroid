package callback

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// ActionHandler defines the signature for a callback action handler function.
type ActionHandler func(ctx context.Context, tx *Transaction) error

// InlineHandler defines the signature for an inline callback action handler function.
type InlineHandler func(ctx context.Context, tx *InlineTransaction) error

// Router dispatches incoming callback transactions to registered handlers with validation and authorization.
type Router struct {
	mu             sync.RWMutex
	handlers       map[string]ActionHandler
	inlineHandlers map[string]InlineHandler
	authorizer     Authorizer
	metrics        core.MetricsCollector
	logger         *zap.Logger
}

// NewRouter creates an initialized callback Router.
func NewRouter(logger *zap.Logger) *Router {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Router{
		handlers:       make(map[string]ActionHandler),
		inlineHandlers: make(map[string]InlineHandler),
		authorizer:     AllowAllAuthorizer{},
		logger:         logger,
	}
}

// SetMetricsCollector configures optional runtime metrics collection.
func (r *Router) SetMetricsCollector(m core.MetricsCollector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = m
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

// RegisterInline attaches a handler to a specific inline namespace and action.
func (r *Router) RegisterInline(namespace, action string, handler InlineHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := fmt.Sprintf("%s:%s", namespace, action)
	r.inlineHandlers[key] = handler
}

// Dispatch processes a CallbackTransaction through authorization, matching, and execution.
func (r *Router) Dispatch(ctx context.Context, tx *Transaction) error {
	if tx == nil {
		return interaction.ErrInvalidTarget
	}

	start := time.Now()
	correlationID := fmt.Sprintf("cb-%d-%d", tx.QueryID, start.UnixNano())
	correlationKey := fmt.Sprintf("%s:%s", tx.Payload.Namespace, tx.Payload.Action)

	// Enforce 15-second execution timeout if not already set
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}

	// Ensure the query is answered even if the handler forgets or panics (Router-owned lifecycle)
	defer func() {
		if !tx.IsAnswered() {
			_ = tx.Answer(ctx, "", false)
		}
	}()

	_ = tx.Transition(StateValidated)

	// 1. Authorization check
	r.mu.RLock()
	authorizer := r.authorizer
	r.mu.RUnlock()

	actor := Actor{
		UserID: tx.UserID,
		ChatID: tx.Target.ChatID(),
	}

	if err := authorizer.Authorize(ctx, actor, tx.Payload.Action); err != nil {
		_ = tx.Answer(ctx, "⚠️ This is OWNER's bot!!", true)
		r.logger.Warn("assistant: unauthorized callback rejected",
			zap.String("correlation_id", correlationID),
			zap.Int64("user_id", tx.UserID),
			zap.String("action", tx.Payload.Action),
			zap.Int64("query_id", tx.QueryID),
		)
		tx.SetState(StateFailed)
		if r.metrics != nil {
			r.metrics.RecordCallback("unauthorized", time.Since(start), err)
		}
		return ErrUnauthorized
	}

	_ = tx.Transition(StateAuthorized)

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
			zap.String("correlation_id", correlationID),
			zap.String("key", correlationKey),
			zap.Int64("query_id", tx.QueryID),
		)
		tx.SetState(StateFailed)
		if r.metrics != nil {
			r.metrics.RecordCallback("invalid", time.Since(start), ErrUnknownAction)
		}
		return fmt.Errorf("%w: %s", ErrUnknownAction, correlationKey)
	}

	// 3. Execute handler
	_ = tx.Transition(StateExecuting)
	err := handler(ctx, tx)

	duration := time.Since(start)
	if err != nil {
		tx.SetState(StateFailed)
		r.logger.Error("assistant: callback handler failed",
			zap.String("correlation_id", correlationID),
			zap.String("key", correlationKey),
			zap.Int64("query_id", tx.QueryID),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
		if r.metrics != nil {
			r.metrics.RecordCallback("handler_error", duration, err)
		}
		return err
	}

	_ = tx.Transition(StateCompleted)
	r.logger.Debug("assistant: callback handler completed",
		zap.String("correlation_id", correlationID),
		zap.String("key", correlationKey),
		zap.Int64("query_id", tx.QueryID),
		zap.Duration("duration", duration),
	)
	if r.metrics != nil {
		r.metrics.RecordCallback("success", duration, nil)
	}
	return nil
}

// DispatchInline processes an InlineTransaction through authorization, matching, and execution.
func (r *Router) DispatchInline(ctx context.Context, tx *InlineTransaction) error {
	if tx == nil {
		return interaction.ErrInvalidTarget
	}

	start := time.Now()
	correlationID := fmt.Sprintf("in-cb-%d-%d", tx.QueryID, start.UnixNano())
	correlationKey := fmt.Sprintf("%s:%s", tx.Payload.Namespace, tx.Payload.Action)

	// Enforce 15-second execution timeout if not already set
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}

	// Ensure the inline query is answered even if the handler forgets or panics (Router-owned lifecycle)
	defer func() {
		if !tx.IsAnswered() {
			_ = tx.Answer(ctx, "", false)
		}
	}()

	_ = tx.Transition(StateValidated)

	// 1. Authorization check
	r.mu.RLock()
	authorizer := r.authorizer
	r.mu.RUnlock()

	actor := Actor{
		UserID: tx.UserID,
		ChatID: 0,
	}

	if err := authorizer.Authorize(ctx, actor, tx.Payload.Action); err != nil {
		_ = tx.Answer(ctx, "⚠️ This is OWNER's bot!!", true)
		r.logger.Warn("assistant: unauthorized inline callback rejected",
			zap.String("correlation_id", correlationID),
			zap.Int64("user_id", tx.UserID),
			zap.String("action", tx.Payload.Action),
			zap.Int64("query_id", tx.QueryID),
		)
		tx.SetState(StateFailed)
		if r.metrics != nil {
			r.metrics.RecordCallback("unauthorized", time.Since(start), err)
			r.metrics.RecordInline(false, 0, time.Since(start), err)
		}
		return ErrUnauthorized
	}

	_ = tx.Transition(StateAuthorized)

	// 2. Resolve inline handler
	r.mu.RLock()
	handler, ok := r.inlineHandlers[correlationKey]
	if !ok {
		// Try wildcard namespace handler
		handler, ok = r.inlineHandlers[fmt.Sprintf("%s:*", tx.Payload.Namespace)]
	}
	r.mu.RUnlock()

	if !ok {
		_ = tx.Answer(ctx, "Action no longer available", false)
		r.logger.Warn("assistant: unknown inline callback action",
			zap.String("correlation_id", correlationID),
			zap.String("key", correlationKey),
			zap.Int64("query_id", tx.QueryID),
		)
		tx.SetState(StateFailed)
		if r.metrics != nil {
			r.metrics.RecordCallback("invalid", time.Since(start), ErrUnknownAction)
			r.metrics.RecordInline(false, 0, time.Since(start), ErrUnknownAction)
		}
		return fmt.Errorf("%w: %s", ErrUnknownAction, correlationKey)
	}

	// 3. Execute handler
	_ = tx.Transition(StateExecuting)
	err := handler(ctx, tx)

	duration := time.Since(start)
	if err != nil {
		tx.SetState(StateFailed)
		r.logger.Error("assistant: inline callback handler failed",
			zap.String("correlation_id", correlationID),
			zap.String("key", correlationKey),
			zap.Int64("query_id", tx.QueryID),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
		if r.metrics != nil {
			r.metrics.RecordCallback("handler_error", duration, err)
			r.metrics.RecordInline(false, 0, duration, err)
		}
		return err
	}

	_ = tx.Transition(StateCompleted)
	r.logger.Debug("assistant: inline callback handler completed",
		zap.String("correlation_id", correlationID),
		zap.String("key", correlationKey),
		zap.Int64("query_id", tx.QueryID),
		zap.Duration("duration", duration),
	)
	if r.metrics != nil {
		r.metrics.RecordCallback("success", duration, nil)
		r.metrics.RecordInline(false, 0, duration, nil)
	}
	return nil
}
