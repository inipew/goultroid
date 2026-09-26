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

const legacyExpiredText = "⌛ Interaction expired. Please reopen it."

// Router is the temporary legacy callback compatibility shell retained until
// P1-F4 removes transport/bootstrap wiring. P1-F3 intentionally ended the v1
// protocol/state compatibility window: only raw noop survives to clear an
// already-visible spinner; every other non-a2 payload is rejected as expired.
type Router struct {
	handlers map[string]registration
	logger   *zap.Logger
	metrics  core.MetricsCollector
	limiter  *ratelimit.Limiter
	timeout  time.Duration
	nextID   uint64
	mu       sync.RWMutex
}

type registration struct {
	handler Handler
	owner   string
	id      uint64
}

// PreparedCallback is the opaque execution lease consumed by the existing
// transport bridges. After P1-F3 only raw noop produces a prepared lease; all
// other non-a2 payloads are rejected during Prepare.
type PreparedCallback interface {
	Scope() tasks.ScopeIdentity
	Dispatch(context.Context, *core.CallbackQueryEvent, core.TelegramServicer) error
}

type preparedDispatch struct {
	router  *Router
	rawData string
	noop    bool
}

func (p *preparedDispatch) Scope() tasks.ScopeIdentity {
	return tasks.ScopeIdentity{}
}

func (p *preparedDispatch) Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	if p == nil || p.router == nil {
		return core.ErrInternal
	}
	return p.router.dispatchPrepared(ctx, evt, svc, p)
}

// Registration is the minimal lifecycle lease returned by RegisterOwned.
// The legacy registration surface is retained only until P1-F4 removes it.
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

const defaultCallbackTimeout = 15 * time.Second

// NewRouter creates the temporary legacy callback compatibility shell.
func NewRouter(logger *zap.Logger) *Router {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Router{
		handlers: make(map[string]registration),
		logger:   logger,
		timeout:  defaultCallbackTimeout,
	}
}

// SetMetrics configures metrics collector for callback compatibility outcomes.
func (r *Router) SetMetrics(m core.MetricsCollector) { r.metrics = m }

// SetLimiter retains the pre-P1-F4 composition contract. P1-F3 no longer
// creates legacy callback buckets because v1 dispatch has been retired.
func (r *Router) SetLimiter(l *ratelimit.Limiter) { r.limiter = l }

// SetTimeout retains the pre-P1-F4 handler configuration contract.
func (r *Router) SetTimeout(d time.Duration) {
	if d > 0 {
		r.timeout = d
	}
}

// RegisterOwned retains the legacy plugin lifecycle surface until P1-F4. No
// production feature currently implements Handler and P1-F3 no longer admits
// namespace payloads into these registrations.
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

// Prepare admits only the raw noop compatibility control. The legacy v1
// namespace protocol/state window ended in P1-F3; all other non-a2 payloads
// are terminally acknowledged as expired before TaskEngine submission.
func (r *Router) Prepare(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	_ func(string) (tasks.ScopeIdentity, bool),
) (PreparedCallback, error) {
	if evt == nil {
		return nil, ErrInvalidCallbackData
	}
	start := time.Now()
	raw := string(evt.Data)
	if raw == ActionNoop {
		return &preparedDispatch{router: r, rawData: raw, noop: true}, nil
	}

	r.logger.Debug("retired legacy callback rejected",
		zap.Int64("query_id", evt.QueryID),
		zap.Int64("user_id", evt.UserID),
		zap.Int64("chat_id", evt.ChatID),
		zap.String("origin", originString(evt.Origin)))
	return nil, r.reject(ctx, evt, svc, callbackFailure{
		Code:        failureCodeHandlerNotFound,
		MetricTag:   "expired_legacy",
		UserAlert:   legacyExpiredText,
		InternalErr: ErrHandlerNotFound,
		IsAlert:     false,
	}, start)
}

// checkRateLimit is retained with the legacy Router until P1-F4. It is no
// longer called by Prepare after v1 admission was removed, so P1-F3 creates no
// new callback-specific buckets in the shared interaction limiter.
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
	return r.reject(ctx, evt, svc, callbackFailure{
		Code:        failureCodeHandlerNotFound,
		MetricTag:   "expired_legacy",
		UserAlert:   legacyExpiredText,
		InternalErr: ErrHandlerNotFound,
		IsAlert:     false,
	}, start)
}

// executeHandler remains only for the P1-F4 Handler/Router removal boundary.
// No production ingress can reach it after P1-F3 retired namespace admission.
func (r *Router) executeHandler(
	ctx context.Context,
	evt *core.CallbackQueryEvent,
	svc core.TelegramServicer,
	handler Handler,
	start time.Time,
) error {
	cbCtx := &CallbackContext{
		Ctx:          ctx,
		QueryID:      evt.QueryID,
		UserID:       evt.UserID,
		ChatID:       evt.ChatID,
		MsgID:        evt.MsgID,
		RawData:      evt.Data,
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
