package callback

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// PreparedCallback is the opaque execution lease returned by Router.Prepare.
// It pins one callback handler registration and its TaskEngine lifecycle scope.
// Callers cannot inspect or mutate the registration identity.
type PreparedCallback interface {
	Scope() tasks.ScopeIdentity
	Dispatch(context.Context, *core.CallbackQueryEvent, core.TelegramServicer) error
}

type preparedDispatch struct {
	router         *Router
	namespace      string
	action         string
	opaqueID       string
	rawData        string
	registrationID uint64
	scope          tasks.ScopeIdentity
	noop           bool
}

func (p *preparedDispatch) Scope() tasks.ScopeIdentity {
	if p == nil {
		return tasks.ScopeIdentity{}
	}
	return p.scope
}

func (p *preparedDispatch) Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	if p == nil || p.router == nil {
		return core.ErrInternal
	}
	return p.router.dispatchPrepared(ctx, evt, svc, p)
}

// Registration is the minimal lifecycle lease returned by RegisterOwned.
// Its concrete registration identity remains private to the callback router.
type Registration interface {
	Close()
}

type registrationLease struct {
	router    *Router
	namespace string
	id        uint64
	once      sync.Once
}

// Close unregisters this exact handler without removing a later replacement.
func (r *registrationLease) Close() {
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

// RegisterOwned registers a handler together with its lifecycle owner.
// Every canonical callback handler must be lifecycle-owned so TaskEngine scope
// admission can fence disable/reload boundaries.
func (r *Router) RegisterOwned(owner string, h Handler) (Registration, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, fmt.Errorf("callback handler owner cannot be empty")
	}
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
	return &registrationLease{router: r, namespace: ns, id: r.nextID}, nil
}

// Prepare validates callback protocol and rate limits, pins one concrete
// registration, resolves its TaskEngine scope, and then revalidates the
// registration before returning. No callback state is read or consumed here.
func (r *Router) Prepare(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	resolve func(string) (tasks.ScopeIdentity, bool),
) (PreparedCallback, error) {
	return r.prepare(ctx, evt, svc, resolve, true)
}

func (r *Router) prepare(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	resolve func(string) (tasks.ScopeIdentity, bool),
	requireScope bool,
) (*preparedDispatch, error) {
	if evt == nil {
		return nil, ErrInvalidCallbackData
	}
	start := time.Now()
	raw := string(evt.Data)
	if raw == ActionNoop {
		return &preparedDispatch{router: r, rawData: raw, noop: true}, nil
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
		return nil, r.reject(ctx, evt, svc, callbackFailure{
			Code:        failureCodeInvalidPayload,
			MetricTag:   "invalid",
			UserAlert:   "Invalid button action",
			InternalErr: ErrInvalidCallbackData,
			IsAlert:     false,
		}, start)
	}
	if err := r.checkRateLimit(ctx, evt, ns, svc, start); err != nil {
		return nil, err
	}
	if action == ActionNoop {
		return &preparedDispatch{
			router:    r,
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
		return nil, r.reject(ctx, evt, svc, callbackFailure{
			Code:        failureCodeHandlerNotFound,
			MetricTag:   "invalid",
			UserAlert:   "Feature not available",
			InternalErr: ErrHandlerNotFound,
			IsAlert:     false,
		}, start)
	}

	var scope tasks.ScopeIdentity
	if requireScope && reg.owner != "" {
		if resolve == nil {
			return nil, r.reject(ctx, evt, svc, callbackFailure{
				Code:        failureCodeHandlerNotFound,
				MetricTag:   "unavailable",
				UserAlert:   "Feature not available.",
				InternalErr: ErrHandlerRegistrationChanged,
				IsAlert:     false,
			}, start)
		}
		var available bool
		scope, available = resolve(reg.owner)
		if !available {
			return nil, r.reject(ctx, evt, svc, callbackFailure{
				Code:        failureCodeHandlerNotFound,
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
		return nil, r.reject(ctx, evt, svc, callbackFailure{
			Code:        failureCodeHandlerNotFound,
			MetricTag:   "stale_registration",
			UserAlert:   "Feature not available.",
			InternalErr: ErrHandlerRegistrationChanged,
			IsAlert:     false,
		}, start)
	}

	return &preparedDispatch{
		router:         r,
		namespace:      ns,
		action:         action,
		opaqueID:       opaqueID,
		rawData:        raw,
		registrationID: reg.id,
		scope:          scope,
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
		return r.reject(ctx, evt, svc, callbackFailure{
			Code:        failureCodeRateLimited,
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
	handlerScope tasks.ScopeIdentity,
	svc core.TelegramServicer,
	start time.Time,
) (any, stateEntry, bool, error) {
	if opaqueID == "" || opaqueID == ActionNoop || opaqueID == "-" || r.stateStore == nil {
		return nil, stateEntry{}, false, nil
	}

	var validationFailure *callbackFailure
	entry, stateErr := r.stateStore.claimEntry(opaqueID, func(scope StateScope) error {
		switch {
		case scope.UserID > 0 && evt.UserID != scope.UserID:
			validationFailure = &callbackFailure{
				Code:        failureCodeUnauthorized,
				MetricTag:   "unauthorized",
				UserAlert:   "⚠️ You are not authorized to use this button.",
				InternalErr: ErrUnauthorized,
				IsAlert:     true,
			}
		case scope.Namespace != "" && scope.Namespace != ns:
			validationFailure = &callbackFailure{
				Code:        failureCodeInvalidPayload,
				MetricTag:   "invalid",
				UserAlert:   "Invalid button scope.",
				InternalErr: ErrInvalidCallbackData,
				IsAlert:     false,
			}
		case !scope.OwnerScope.IsZero() && scope.OwnerScope != handlerScope:
			validationFailure = &callbackFailure{
				Code:        failureCodeSessionExpired,
				MetricTag:   "stale_generation",
				UserAlert:   "⏰ Button expired, run the command again.",
				InternalErr: ErrStateScopeStale,
				IsAlert:     true,
			}
		case scope.ChatID != 0 && scope.ChatID != evt.ChatID:
			validationFailure = &callbackFailure{
				Code:        failureCodeUnauthorized,
				MetricTag:   "unauthorized",
				UserAlert:   "Button not valid in this chat.",
				InternalErr: ErrUnauthorized,
				IsAlert:     true,
			}
		case scope.MessageID != 0 && scope.MessageID != evt.Target.MessageID:
			validationFailure = &callbackFailure{
				Code:        failureCodeUnauthorized,
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
		return nil, stateEntry{}, false, r.reject(ctx, evt, svc, *validationFailure, start)
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
		return nil, stateEntry{}, false, r.reject(ctx, evt, svc, callbackFailure{
			Code:        failureCodeSessionExpired,
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
		return nil, stateEntry{}, false, r.reject(ctx, evt, svc, callbackFailure{
			Code:        failureCodeSessionExpired,
			MetricTag:   "invalid",
			UserAlert:   "Button already used.",
			InternalErr: ErrStateNotFound,
			IsAlert:     true,
		}, start)
	case errors.Is(stateErr, ErrStateNotFound):
		return nil, stateEntry{}, false, nil
	default:
		r.logger.Warn("callback state lookup failed",
			zap.Int64("query_id", evt.QueryID),
			zap.String("namespace", ns),
			zap.Error(stateErr))
		return nil, stateEntry{}, false, nil
	}
}

// dispatchPrepared executes exactly the handler registration pinned by Prepare.
// If the plugin was disabled/reloaded after admission, the stale registration
// is rejected before callback state can be claimed or consumed.
func (r *Router) dispatchPrepared(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	prepared *preparedDispatch,
) error {
	if evt == nil || prepared == nil {
		return core.ErrInternal
	}
	start := time.Now()
	if prepared.router != r || prepared.rawData != string(evt.Data) {
		return r.reject(ctx, evt, svc, callbackFailure{
			Code:        failureCodeInvalidPayload,
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
		return r.reject(ctx, evt, svc, callbackFailure{
			Code:        failureCodeHandlerNotFound,
			MetricTag:   "stale_registration",
			UserAlert:   "Feature not available.",
			InternalErr: ErrHandlerRegistrationChanged,
			IsAlert:     false,
		}, start)
	}
	handler := reg.handler
	r.mu.RUnlock()

	storedState, _, hasState, err := r.resolveState(ctx, evt, prepared.namespace, prepared.action, prepared.opaqueID, prepared.scope, svc, start)
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
		return r.reject(ctx, evt, svc, callbackFailure{
			Code:        failureCodeSessionExpired,
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
	chain := chain(handler, recoverMiddleware(r.logger), timeoutMiddleware(timeout))
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
func (r *Router) reject(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer, failure callbackFailure, start time.Time) error {
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
