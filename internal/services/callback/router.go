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

// Dispatch processes an incoming CallbackQueryEvent from the domain event bus.
func (r *Router) Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	if evt == nil {
		return nil
	}
	start := time.Now()
	if r.metrics != nil {
		defer func() {
			// generic received counter handled per branch; this defers no-op, individual branches record specific status
			_ = start
		}()
	}

	// Direct raw noop check (e.g. pagination indicators: []byte("noop"))
	if string(evt.Data) == "noop" {
		if svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false)
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

	// Rate limiting per user (separate from command limiter)
	if r.limiter != nil {
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
	}

	// No-op buttons (e.g. page counter indicators)
	if action == ActionNoop || opaqueID == ActionNoop {
		if svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false)
		}
		return nil
	}

	var storedState any
	var entry StateEntry
	var hasState bool
	if opaqueID != "" && r.stateStore != nil {
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
				return r.reject(ctx, evt, svc, CallbackFailure{
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
				return r.reject(ctx, evt, svc, CallbackFailure{
					Code:        FailureCodeSessionExpired,
					MetricTag:   "invalid",
					UserAlert:   "Button already used.",
					InternalErr: ErrStateNotFound,
					IsAlert:     true,
				}, start)
			}
			if errors.Is(getErr, ErrStateNotFound) {
				// No state is not an error for stateless callbacks; just continue without state.
				// We still allow handler dispatch; scope checks only if state exists.
			} else {
				r.logger.Warn("callback state lookup failed",
					zap.Int64("query_id", evt.QueryID),
					zap.String("namespace", ns),
					zap.Error(getErr))
			}
		} else {
			hasState = true
			entry = e
			storedState = e.Data
			// Authorization: user
			if e.Scope.UserID > 0 && evt.UserID != e.Scope.UserID {
				r.logger.Warn("callback unauthorized",
					zap.Int64("query_id", evt.QueryID),
					zap.Int64("user_id", evt.UserID),
					zap.Int64("allowed_user", e.Scope.UserID),
					zap.String("namespace", ns),
					zap.String("action", action),
					zap.String("opaque_id", opaqueID))
				return r.reject(ctx, evt, svc, CallbackFailure{
					Code:        FailureCodeUnauthorized,
					MetricTag:   "unauthorized",
					UserAlert:   "⚠️ You are not authorized to use this button.",
					InternalErr: ErrUnauthorized,
					IsAlert:     true,
				}, start)
			}
			// Scope: namespace binding
			if e.Scope.Namespace != "" && e.Scope.Namespace != ns {
				r.logger.Warn("callback namespace scope mismatch",
					zap.String("expected", e.Scope.Namespace),
					zap.String("got", ns),
					zap.String("opaque_id", opaqueID))
				return r.reject(ctx, evt, svc, CallbackFailure{
					Code:        FailureCodeInvalidPayload,
					MetricTag:   "invalid",
					UserAlert:   "Invalid button scope.",
					InternalErr: ErrInvalidCallbackData,
					IsAlert:     false,
				}, start)
			}
			// Scope: chat / message binding if set
			if e.Scope.ChatID != 0 && e.Scope.ChatID != evt.ChatID {
				r.logger.Warn("callback chat scope mismatch",
					zap.Int64("expected_chat", e.Scope.ChatID),
					zap.Int64("got_chat", evt.ChatID),
					zap.String("opaque_id", opaqueID))
				return r.reject(ctx, evt, svc, CallbackFailure{
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
				return r.reject(ctx, evt, svc, CallbackFailure{
					Code:        FailureCodeUnauthorized,
					MetricTag:   "unauthorized",
					UserAlert:   "Button not valid for this message.",
					InternalErr: ErrUnauthorized,
					IsAlert:     true,
				}, start)
			}
			// SingleUse: atomically consume before handler
			if e.Scope.SingleUse {
				if _, cErr := r.stateStore.Consume(opaqueID); cErr != nil {
					if errors.Is(cErr, ErrStateExpired) {
						return r.reject(ctx, evt, svc, CallbackFailure{
							Code:        FailureCodeSessionExpired,
							MetricTag:   "expired",
							UserAlert:   "⏰ Button expired.",
							InternalErr: ErrStateExpired,
							IsAlert:     true,
						}, start)
					}
					return r.reject(ctx, evt, svc, CallbackFailure{
						Code:        FailureCodeSessionExpired,
						MetricTag:   "invalid",
						UserAlert:   "Button already used.",
						InternalErr: ErrStateNotFound,
						IsAlert:     true,
					}, start)
				}
			}
		}
		_ = hasState
		_ = entry
	}

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

	// Standard UX 3.1: immediate ack to clear loading state unless handler opts out
	opts := handlerOptions(handler)
	if opts.AutoAnswer && svc != nil && !cbCtx.IsAnswered() {
		_ = cbCtx.Answer(opts.DefaultText, opts.DefaultAlert)
	}

	// Timeout + panic recovery around handler
	handleErr := func() (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				r.logger.Error("callback handler panic",
					zap.Any("panic", rec),
					zap.String("namespace", ns),
					zap.String("action", action),
					zap.Int64("query_id", evt.QueryID))
				err = fmt.Errorf("%w: handler panic: %v", core.ErrInternal, rec)
				if r.metrics != nil {
					r.metrics.RecordCallback("handler_panic", time.Since(start), err)
				}
			}
		}()
		timeout := r.timeout
		if timeout <= 0 {
			timeout = defaultCallbackTimeout
		}
		hCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cbCtx.Ctx = hCtx
		err = handler.HandleCallback(cbCtx)
		return err
	}()

	if r.metrics != nil {
		status := "success"
		if handleErr != nil {
			status = "handler_error"
		}
		r.metrics.RecordCallback(status, time.Since(start), handleErr)
	}

	// Fallback only if handler hasn't answered and we didn't auto-answer
	if !cbCtx.IsAnswered() && svc != nil {
		if handleErr != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Action failed", false)
		} else {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false)
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
		_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, failure.UserAlert, failure.IsAlert)
	}
	return failure.InternalErr
}

