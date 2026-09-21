package callback

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

// Router routes incoming callback query events to registered namespace handlers.
type Router struct {
	handlers   map[string]registration
	stateStore *StateStore
	logger     *zap.Logger
	metrics    core.MetricsCollector
	limiter    *ratelimit.Limiter
	timeout    time.Duration
	nextID     uint64
	mu         sync.RWMutex
}

type registration struct {
	handler Handler
	owner   string
	id      uint64
}

// PreparedDispatch pins callback admission to one concrete handler
// registration. Scope is resolved before TaskEngine admission; DispatchPrepared
// rejects the callback if that registration is no longer current.
type PreparedDispatch struct {
	namespace      string
	action         string
	opaqueID       string
	rawData        string
	registrationID uint64
	ResolvedScope  tasks.ScopeIdentity
	noop           bool
}

// Scope returns the TaskEngine lifecycle scope resolved for this callback.
func (p PreparedDispatch) Scope() tasks.ScopeIdentity { return p.ResolvedScope }

// Registration is an idempotent callback handler lease.
type Registration struct {
	router    *Router
	namespace string
	id        uint64
	once      sync.Once
}

// Close unregisters this exact handler without removing a later replacement.
func (r *Registration) Close() {
	if r == nil || r.router == nil {
		return
	}
	r.once.Do(func() {
		r.router.mu.Lock()
		if current, ok := r.router.handlers[r.namespace]; ok && current.id == r.id {
			delete(r.router.handlers, r.namespace)
		}
		r.router.mu.Unlock()
	})
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
		handlers:   make(map[string]registration),
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
	_, err := r.RegisterOwned("", h)
	return err
}

// RegisterOwned registers a handler together with its lifecycle owner.
func (r *Router) RegisterOwned(owner string, h Handler) (*Registration, error) {
	if h == nil {
		return nil, fmt.Errorf("handler cannot be nil")
	}
	ns := h.Namespace()
	if ns == "" {
		return nil, fmt.Errorf("handler namespace cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.handlers[ns]; exists {
		return nil, fmt.Errorf("callback handler for namespace %q is already registered", ns)
	}

	r.nextID++
	r.handlers[ns] = registration{handler: h, owner: owner, id: r.nextID}
	return &Registration{router: r, namespace: ns, id: r.nextID}, nil
}

// GetHandler retrieves the registered handler for a namespace.
func (r *Router) GetHandler(namespace string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.handlers[namespace]
	return reg.handler, ok
}

// HasHandler checks whether a handler is registered for the namespace.
func (r *Router) HasHandler(namespace string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.handlers[namespace]
	return ok
}

// TaskScope resolves callback payload ownership before task admission.
//
// Deprecated for execution paths that can use Prepare. TaskScope is retained
// for compatibility, but now fails closed if the handler disappears or is
// replaced while ownership is being resolved.
func (r *Router) TaskScope(data []byte, resolve func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool) {
	ns, action, _, err := ParseCallbackData(data)
	if err != nil {
		return tasks.ScopeIdentity{}, true // malformed data is rejected by Dispatch.
	}
	if action == ActionNoop {
		return tasks.ScopeIdentity{}, true
	}

	r.mu.RLock()
	reg, ok := r.handlers[ns]
	r.mu.RUnlock()
	if !ok {
		return tasks.ScopeIdentity{}, false
	}
	if reg.owner == "" {
		return tasks.ScopeIdentity{}, true
	}
	if resolve == nil {
		return tasks.ScopeIdentity{}, false
	}

	scope, available := resolve(reg.owner)
	if !available {
		return tasks.ScopeIdentity{}, false
	}

	r.mu.RLock()
	current, stillCurrent := r.handlers[ns]
	r.mu.RUnlock()
	if !stillCurrent || current.id != reg.id {
		return tasks.ScopeIdentity{}, false
	}
	return scope, true
}

// Prepare validates callback protocol and rate limits, pins one concrete
// registration, resolves its TaskEngine scope, and then revalidates the
// registration before returning. No callback state is read or consumed here.
func (r *Router) Prepare(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	resolve func(string) (tasks.ScopeIdentity, bool),
) (PreparedDispatch, error) {
	return r.prepare(ctx, evt, svc, resolve, true)
}

func (r *Router) prepare(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	resolve func(string) (tasks.ScopeIdentity, bool),
	requireScope bool,
) (PreparedDispatch, error) {
	if evt == nil {
		return PreparedDispatch{}, ErrInvalidCallbackData
	}
	start := time.Now()
	raw := string(evt.Data)
	if raw == ActionNoop {
		return PreparedDispatch{rawData: raw, noop: true}, nil
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
		return PreparedDispatch{}, r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeInvalidPayload,
			MetricTag:   "invalid",
			UserAlert:   "Invalid button action",
			InternalErr: ErrInvalidCallbackData,
			IsAlert:     false,
		}, start)
	}
	if err := r.checkRateLimit(ctx, evt, ns, svc, start); err != nil {
		return PreparedDispatch{}, err
	}
	if action == ActionNoop {
		return PreparedDispatch{
			namespace: ns,
			action:    action,
			opaqueID:  opaqueID,
			rawData:   raw,
			noop:      true,
		}, nil
	}

	r.mu.RLock()
	reg, ok := r.handlers[ns]
	r.mu.RUnlock()
	if !ok {
		return PreparedDispatch{}, r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeHandlerNotFound,
			MetricTag:   "invalid",
			UserAlert:   "Feature not available",
			InternalErr: ErrHandlerNotFound,
			IsAlert:     false,
		}, start)
	}

	var scope tasks.ScopeIdentity
	if requireScope && reg.owner != "" {
		if resolve == nil {
			return PreparedDispatch{}, r.reject(ctx, evt, svc, CallbackFailure{
				Code:        FailureCodeHandlerNotFound,
				MetricTag:   "unavailable",
				UserAlert:   "Feature not available.",
				InternalErr: ErrHandlerRegistrationChanged,
				IsAlert:     false,
			}, start)
		}
		var available bool
		scope, available = resolve(reg.owner)
		if !available {
			return PreparedDispatch{}, r.reject(ctx, evt, svc, CallbackFailure{
				Code:        FailureCodeHandlerNotFound,
				MetricTag:   "unavailable",
				UserAlert:   "Feature not available.",
				InternalErr: ErrHandlerRegistrationChanged,
				IsAlert:     false,
			}, start)
		}
	}

	r.mu.RLock()
	current, stillCurrent := r.handlers[ns]
	r.mu.RUnlock()
	if !stillCurrent || current.id != reg.id {
		return PreparedDispatch{}, r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeHandlerNotFound,
			MetricTag:   "stale_registration",
			UserAlert:   "Feature not available.",
			InternalErr: ErrHandlerRegistrationChanged,
			IsAlert:     false,
		}, start)
	}

	return PreparedDispatch{
		namespace:      ns,
		action:         action,
		opaqueID:       opaqueID,
		rawData:        raw,
		registrationID: reg.id,
		ResolvedScope:  scope,
	}, nil
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

// resolveState atomically validates callback scope and claims single-use
// state only after authorization succeeds.
func (r *Router) resolveState(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	ns, action, opaqueID string,
	svc core.TelegramServicer,
	start time.Time,
) (any, StateEntry, bool, error) {
	if opaqueID == "" || opaqueID == ActionNoop || opaqueID == "-" || r.stateStore == nil {
		return nil, StateEntry{}, false, nil
	}

	var validationFailure *CallbackFailure
	entry, stateErr := r.stateStore.ClaimEntry(opaqueID, func(scope StateScope) error {
		switch {
		case scope.UserID > 0 && evt.UserID != scope.UserID:
			validationFailure = &CallbackFailure{
				Code:        FailureCodeUnauthorized,
				MetricTag:   "unauthorized",
				UserAlert:   "⚠️ You are not authorized to use this button.",
				InternalErr: ErrUnauthorized,
				IsAlert:     true,
			}
		case scope.Namespace != "" && scope.Namespace != ns:
			validationFailure = &CallbackFailure{
				Code:        FailureCodeInvalidPayload,
				MetricTag:   "invalid",
				UserAlert:   "Invalid button scope.",
				InternalErr: ErrInvalidCallbackData,
				IsAlert:     false,
			}
		case scope.ChatID != 0 && scope.ChatID != evt.ChatID:
			validationFailure = &CallbackFailure{
				Code:        FailureCodeUnauthorized,
				MetricTag:   "unauthorized",
				UserAlert:   "Button not valid in this chat.",
				InternalErr: ErrUnauthorized,
				IsAlert:     true,
			}
		case scope.MessageID != 0 && scope.MessageID != evt.Target.MessageID:
			validationFailure = &CallbackFailure{
				Code:        FailureCodeUnauthorized,
				MetricTag:   "unauthorized",
				UserAlert:   "Button not valid for this message.",
				InternalErr: ErrUnauthorized,
				IsAlert:     true,
			}
		}
		if validationFailure != nil {
			return validationFailure.InternalErr
		}
		return nil
	})
	if stateErr == nil {
		return entry.Data, entry, true, nil
	}
	if validationFailure != nil {
		r.logger.Warn("callback state scope rejected",
			zap.Int64("query_id", evt.QueryID),
			zap.Int64("user_id", evt.UserID),
			zap.String("namespace", ns),
			zap.String("action", action),
			zap.String("opaque_id", opaqueID),
			zap.String("code", string(validationFailure.Code)))
		return nil, StateEntry{}, false, r.reject(ctx, evt, svc, *validationFailure, start)
	}

	switch {
	case errors.Is(stateErr, ErrStateExpired):
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
	case errors.Is(stateErr, ErrStateConsumed):
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
	case errors.Is(stateErr, ErrStateNotFound):
		return nil, StateEntry{}, false, nil
	default:
		r.logger.Warn("callback state lookup failed",
			zap.Int64("query_id", evt.QueryID),
			zap.String("namespace", ns),
			zap.Error(stateErr))
		return nil, StateEntry{}, false, nil
	}
}

// Dispatch processes a callback directly without TaskEngine scope admission.
// TaskEngine-backed transports should call Prepare followed by DispatchPrepared.
func (r *Router) Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	prepared, err := r.prepare(ctx, evt, svc, nil, false)
	if err != nil {
		return err
	}
	return r.DispatchPrepared(ctx, evt, svc, prepared)
}

// DispatchPrepared executes exactly the handler registration pinned by Prepare.
// If the plugin was disabled/reloaded after admission, the stale registration
// is rejected before callback state can be claimed or consumed.
func (r *Router) DispatchPrepared(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	prepared PreparedDispatch,
) error {
	if evt == nil {
		return nil
	}
	start := time.Now()
	if prepared.rawData != string(evt.Data) {
		return r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeInvalidPayload,
			MetricTag:   "invalid",
			UserAlert:   "Invalid button action",
			InternalErr: ErrInvalidCallbackData,
			IsAlert:     false,
		}, start)
	}
	if prepared.noop {
		if svc != nil {
			if err := svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false); err != nil {
				r.logger.Debug("answer noop failed", zap.Error(err), zap.Int64("query_id", evt.QueryID))
			}
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.RLock()
	reg, ok := r.handlers[prepared.namespace]
	if !ok || reg.id != prepared.registrationID {
		r.mu.RUnlock()
		return r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeHandlerNotFound,
			MetricTag:   "stale_registration",
			UserAlert:   "Feature not available.",
			InternalErr: ErrHandlerRegistrationChanged,
			IsAlert:     false,
		}, start)
	}
	handler := reg.handler
	r.mu.RUnlock()

	storedState, _, hasState, err := r.resolveState(ctx, evt, prepared.namespace, prepared.action, prepared.opaqueID, svc, start)
	if err != nil {
		return err
	}

	if !hasState && requiresHandlerState(handler, prepared.action, prepared.opaqueID) {
		r.logger.Debug("callback requires state but none is available",
			zap.Int64("query_id", evt.QueryID),
			zap.Int64("user_id", evt.UserID),
			zap.String("namespace", prepared.namespace),
			zap.String("action", prepared.action),
			zap.String("opaque_id", prepared.opaqueID))
		return r.reject(ctx, evt, svc, CallbackFailure{
			Code:        FailureCodeSessionExpired,
			MetricTag:   "missing_state",
			UserAlert:   "⏰ Button expired, run the command again.",
			InternalErr: ErrStateNotFound,
			IsAlert:     true,
		}, start)
	}

	return r.executeHandler(ctx, evt, svc, handler, prepared.namespace, prepared.action, prepared.opaqueID, storedState, start)
}

// executeHandler runs the registration already validated for this dispatch.
func (r *Router) executeHandler(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	handler Handler,
	ns, action, opaqueID string,
	storedState any,
	start time.Time,
) error {
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

	timeout := r.timeout
	if timeout <= 0 {
		timeout = defaultCallbackTimeout
	}
	chain := Chain(handler, RecoverMiddleware(r.logger), TimeoutMiddleware(timeout))
	handleErr := chain.HandleCallback(cbCtx)

	if r.metrics != nil {
		status := "success"
		switch {
		case errors.Is(handleErr, ErrHandlerPanic):
			status = "handler_panic"
		case handleErr != nil:
			status = "handler_error"
		}
		r.metrics.RecordCallback(status, time.Since(start), handleErr)
	}

	if !cbCtx.IsAnswered() && svc != nil {
		var answerErr error
		if handleErr != nil {
			answerErr = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Action failed", false)
		} else {
			answerErr = svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false)
		}
		if answerErr != nil {
			r.logger.Debug("fallback answer failed", zap.Error(answerErr), zap.Int64("query_id", evt.QueryID))
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
