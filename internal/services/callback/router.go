package callback

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"go.uber.org/zap"
)

// Router routes incoming callback query events to registered namespace handlers.
type Router struct {
	handlers   map[string]Handler
	stateStore *StateStore
	logger     *zap.Logger
	metrics    core.MetricsCollector
	limiter    *ratelimit.Limiter
	timeout    time.Duration
	mu         sync.RWMutex
}

const (
	defaultCallbackTimeout = 15 * time.Second
	maxCallbackTextLen     = 4096
)

// NewRouter creates a new callback Router.
func NewRouter(logger *zap.Logger, stateStore *StateStore) *Router {
	if logger == nil {
		logger = zap.NewNop()
	}
	if stateStore == nil {
		stateStore = NewStateStore()
	}
	return &Router{
		handlers:   make(map[string]Handler),
		stateStore: stateStore,
		logger:     logger,
		timeout:    defaultCallbackTimeout,
	}
}

// SetMetrics configures metrics collector for callback interactions.
func (r *Router) SetMetrics(m core.MetricsCollector) { r.metrics = m }

// SetLimiter configures rate limiter for callback queries.
func (r *Router) SetLimiter(l *ratelimit.Limiter) { r.limiter = l }

// SetTimeout configures per-handler timeout.
func (r *Router) SetTimeout(d time.Duration) {
	if d > 0 {
		r.timeout = d
	}
}

// StateStore returns the underlying temporary state store.
func (r *Router) StateStore() *StateStore {
	return r.stateStore
}

// Register registers a new callback handler for its designated namespace.
func (r *Router) Register(h Handler) error {
	if h == nil {
		return fmt.Errorf("handler cannot be nil")
	}
	ns := h.Namespace()
	if ns == "" {
		return fmt.Errorf("handler namespace cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.handlers[ns]; exists {
		return fmt.Errorf("callback handler for namespace %q is already registered", ns)
	}

	r.handlers[ns] = h
	return nil
}

// GetHandler retrieves the registered handler for a namespace.
func (r *Router) GetHandler(namespace string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[namespace]
	return h, ok
}

// checkRateLimit enforces per-user callback rate limiting. Returns reject error if limited.
func (r *Router) checkRateLimit(ctx context.Context, evt *core.CallbackQueryEvent, ns string, svc core.TelegramServicer, start time.Time) error {
	if r.limiter == nil {
		return nil
	}
	key := fmt.Sprintf("%d", evt.UserID)
	if !r.limiter.Allow(ratelimit.DimensionOperation, "callback:"+key) {
		r.logger.Debug("callback rate limited", zap.Int64("user_id", evt.UserID), zap.String("namespace", ns))
		return r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeRateLimited,
			MetricTag:   "rate_limited",
			UserAlert:   "⏳ Too many clicks, slow down.",
			InternalErr: fmt.Errorf("%w: callback rate limited", core.ErrRateLimited),
			IsAlert:     true,
		}, start)
	}
	return nil
}

// resolveState looks up opaqueID state, validates expiry/consumed, and performs authorization and scope checks.
// Returns storedState, entry, hasState, and any reject error.
func (r *Router) resolveState(ctx context.Context, evt *core.CallbackQueryEvent, ns, action, opaqueID string, svc core.TelegramServicer, start time.Time) (any, StateEntry, bool, error) {
	if opaqueID == "" || opaqueID == ActionNoop || opaqueID == "-" || r.stateStore == nil {
		return nil, StateEntry{}, false, nil
	}
	e, getErr := r.stateStore.GetEntry(opaqueID)
	if getErr != nil {
		if errors.Is(getErr, ErrStateExpired) {
			r.logger.Debug("callback state expired",
				zap.Int64("query_id", evt.QueryID),
				zap.Int64("user_id", evt.UserID),
				zap.String("namespace", ns),
				zap.String("action", action),
				zap.String("opaque_id", opaqueID),
				zap.String("origin", originString(evt.Origin)))
			return nil, StateEntry{}, false, r.reject(ctx, evt, svc, CallbackFailure{
				Code:        FailureCodeSessionExpired,
				MetricTag:   "expired",
				UserAlert:   "⏰ Button expired, run the command again.",
				InternalErr: ErrStateExpired,
				IsAlert:     true,
			}, start)
		}
		if errors.Is(getErr, ErrStateConsumed) {
			r.logger.Debug("callback state already consumed",
				zap.Int64("query_id", evt.QueryID),
				zap.Int64("user_id", evt.UserID),
				zap.String("namespace", ns),
				zap.String("action", action),
				zap.String("opaque_id", opaqueID),
				zap.String("origin", originString(evt.Origin)))
			return nil, StateEntry{}, false, r.reject(ctx, evt, svc, CallbackFailure{
				Code:        FailureCodeSessionExpired,
				MetricTag:   "invalid",
				UserAlert:   "Button already used.",
				InternalErr: ErrStateNotFound,
				IsAlert:     true,
			}, start)
		}
		if errors.Is(getErr, ErrStateNotFound) {
			return nil, StateEntry{}, false, nil
		}
		r.logger.Warn("callback state lookup failed",
			zap.Int64("query_id", evt.QueryID),
			zap.String("namespace", ns),
			zap.Error(getErr))
		return nil, StateEntry{}, false, nil
	}
	// Authorization: user
	if e.Scope.UserID > 0 && evt.UserID != e.Scope.UserID {
		r.logger.Warn("callback unauthorized",
			zap.Int64("query_id", evt.QueryID),
			zap.Int64("user_id", evt.UserID),
			zap.Int64("allowed_user", e.Scope.UserID),
			zap.String("namespace", ns),
			zap.String("action", action),
			zap.String("opaque_id", opaqueID))
		return nil, StateEntry{}, false, r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeUnauthorized,
			MetricTag:   "unauthorized",
			UserAlert:   "⚠️ You are not authorized to use this button.",
			InternalErr: ErrUnauthorized,
			IsAlert:     true,
		}, start)
	}
	if e.Scope.Namespace != "" && e.Scope.Namespace != ns {
		r.logger.Warn("callback namespace scope mismatch",
			zap.String("expected", e.Scope.Namespace),
			zap.String("got", ns),
			zap.String("opaque_id", opaqueID))
		return nil, StateEntry{}, false, r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeInvalidPayload,
			MetricTag:   "invalid",
			UserAlert:   "Invalid button scope.",
			InternalErr: ErrInvalidCallbackData,
			IsAlert:     false,
		}, start)
	}
	if e.Scope.ChatID != 0 && e.Scope.ChatID != evt.ChatID {
		r.logger.Warn("callback chat scope mismatch",
			zap.Int64("expected_chat", e.Scope.ChatID),
			zap.Int64("got_chat", evt.ChatID),
			zap.String("opaque_id", opaqueID))
		return nil, StateEntry{}, false, r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeUnauthorized,
			MetricTag:   "unauthorized",
			UserAlert:   "Button not valid in this chat.",
			InternalErr: ErrUnauthorized,
			IsAlert:     true,
		}, start)
	}
	if e.Scope.MessageID != 0 && e.Scope.MessageID != evt.Target.MessageID {
		r.logger.Warn("callback message scope mismatch",
			zap.Int("expected_msg", e.Scope.MessageID),
			zap.Int("got_msg", evt.Target.MessageID))
		return nil, StateEntry{}, false, r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeUnauthorized,
			MetricTag:   "unauthorized",
			UserAlert:   "Button not valid for this message.",
			InternalErr: ErrUnauthorized,
			IsAlert:     true,
		}, start)
	}
	if e.Scope.SingleUse {
		if _, cErr := r.stateStore.Consume(opaqueID); cErr != nil {
			if errors.Is(cErr, ErrStateExpired) {
				return nil, StateEntry{}, false, r.reject(ctx, evt, svc, CallbackFailure{
					Code:        FailureCodeSessionExpired,
					MetricTag:   "expired",
					UserAlert:   "⏰ Button expired.",
					InternalErr: ErrStateExpired,
					IsAlert:     true,
				}, start)
			}
			return nil, StateEntry{}, false, r.reject(ctx, evt, svc, CallbackFailure{
				Code:        FailureCodeSessionExpired,
				MetricTag:   "invalid",
				UserAlert:   "Button already used.",
				InternalErr: ErrStateNotFound,
				IsAlert:     true,
			}, start)
		}
	}
	return e.Data, e, true, nil
}

// Dispatch processes an incoming CallbackQueryEvent from the domain event bus.
func (r *Router) Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	if evt == nil {
		return nil
	}
	start := time.Now()
	if r.metrics != nil {
		defer func() { _ = start }()
	}
	if string(evt.Data) == ActionNoop {
		if svc != nil {
			if err := svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false); err != nil {
				r.logger.Debug("answer noop failed", zap.Error(err), zap.Int64("query_id", evt.QueryID))
			}
		}
		return nil
	}
	ns, action, opaqueID, err := ParseCallbackData(evt.Data)
	if err != nil {
		r.logger.Debug("unrecognized callback data format",
			zap.Int64("query_id", evt.QueryID),
			zap.Int64("user_id", evt.UserID),
			zap.Int64("chat_id", evt.ChatID),
			zap.String("origin", originString(evt.Origin)),
			zap.ByteString("data", evt.Data),
			zap.Error(err))
		return r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeInvalidPayload,
			MetricTag:   "invalid",
			UserAlert:   "Invalid button action",
			InternalErr: ErrInvalidCallbackData,
			IsAlert:     false,
		}, start)
	}
	if err := r.checkRateLimit(ctx, evt, ns, svc, start); err != nil {
		return err
	}
	if action == ActionNoop {
		if svc != nil {
			if err := svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false); err != nil {
				r.logger.Debug("answer noop action failed", zap.Error(err), zap.Int64("query_id", evt.QueryID))
			}
		}
		return nil
	}
	storedState, entry, hasState, err := r.resolveState(ctx, evt, ns, action, opaqueID, svc, start)
	if err != nil {
		return err
	}
	_ = hasState
	_ = entry

	return r.executeHandler(ctx, evt, svc, ns, action, opaqueID, storedState, start)
}

// executeHandler looks up the handler and runs it with timeout, panic recovery, and metrics.
func (r *Router) executeHandler(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer, ns, action, opaqueID string, storedState any, start time.Time) error {
	handler, exists := r.GetHandler(ns)
	if !exists {
		r.logger.Warn("no callback handler for namespace",
			zap.String("namespace", ns),
			zap.String("action", action),
			zap.String("opaque_id", opaqueID),
			zap.Int64("query_id", evt.QueryID),
			zap.Int64("user_id", evt.UserID),
			zap.String("origin", originString(evt.Origin)))
		return r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeHandlerNotFound,
			MetricTag:   "invalid",
			UserAlert:   "Feature not available",
			InternalErr: ErrHandlerNotFound,
			IsAlert:     false,
		}, start)
	}

	cbCtx := &CallbackContext{
		Ctx:          ctx,
		QueryID:      evt.QueryID,
		UserID:       evt.UserID,
		ChatID:       evt.ChatID,
		MsgID:        evt.MsgID,
		RawData:      evt.Data,
		Namespace:    ns,
		Action:       action,
		OpaqueID:     opaqueID,
		State:        storedState,
		Service:      svc,
		Origin:       evt.Origin,
		Target:       evt.Target,
		ChatInstance: evt.ChatInstance,
	}

	opts := handlerOptions(handler)
	if opts.AutoAnswer && svc != nil && !cbCtx.IsAnswered() {
		if err := cbCtx.Answer(opts.DefaultText, opts.DefaultAlert); err != nil {
			r.logger.Debug("auto answer failed", zap.Error(err), zap.Int64("query_id", evt.QueryID))
		}
	}

	// Canonical single execution path: Recover (outermost) → Timeout → Handler
	// Recover must wrap Timeout so panics from timeout/handler are both caught and metrics recorded in middleware.
	timeout := r.timeout
	if timeout <= 0 {
		timeout = defaultCallbackTimeout
	}
	final := handler
	chain := Chain(final, RecoverMiddleware(r.logger, r.metrics, start), TimeoutMiddleware(timeout))
	handleErr := chain.HandleCallback(cbCtx)

	if r.metrics != nil {
		status := "success"
		if handleErr != nil {
			status = "handler_error"
		}
		r.metrics.RecordCallback(status, time.Since(start), handleErr)
	}

	if !cbCtx.IsAnswered() && svc != nil {
		var aErr error
		if handleErr != nil {
			aErr = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Action failed", false)
		} else {
			aErr = svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false)
		}
		if aErr != nil {
			r.logger.Debug("fallback answer failed", zap.Error(aErr), zap.Int64("query_id", evt.QueryID))
		}
	}

	return handleErr
}

func originString(o core.CallbackOrigin) string {
	if o == core.CallbackOriginInline {
		return "inline"
	}
	return "message"
}

// reject records failure metrics, sends user feedback via AnswerCallbackQuery, and returns the underlying error.
func (r *Router) reject(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer, failure CallbackFailure, start time.Time) error {
	if r.metrics != nil {
		metricTag := failure.MetricTag
		if metricTag == "" {
			metricTag = string(failure.Code)
		}
		r.metrics.RecordCallback(metricTag, time.Since(start), failure.InternalErr)
	}
	if svc != nil && failure.UserAlert != "" {
		if err := svc.AnswerCallbackQuery(ctx, evt.QueryID, failure.UserAlert, failure.IsAlert); err != nil {
			r.logger.Debug("reject answer failed", zap.Error(err), zap.Int64("query_id", evt.QueryID), zap.String("code", string(failure.Code)))
		}
	}
	return failure.InternalErr
}
