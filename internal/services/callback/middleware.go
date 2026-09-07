package callback

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// Middleware wraps a Handler with cross-cutting logic (rate limit, auth, timeout, recovery).
// This is the preparation for the full pipeline chain in Fase 2; currently stubs.
type Middleware func(Handler) Handler

// Chain builds a Handler chain from middlewares. Last middleware wraps the final handler first.
func Chain(final Handler, mws ...Middleware) Handler {
	h := final
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// RecoverMiddleware returns a Middleware that recovers panics, logs, records metrics, and converts them to ErrInternal.
// It must be the outermost middleware so panics from inner middlewares (e.g. Timeout) are also caught.
func RecoverMiddleware(logger *zap.Logger, metrics interface{ RecordCallback(string, time.Duration, error) }, start time.Time) Middleware {
	return func(next Handler) Handler {
		return handlerFunc{
			ns: next.Namespace(),
			fn: func(ctx *CallbackContext) (err error) {
				defer func() {
					if rec := recover(); rec != nil {
						if logger != nil {
							logger.Error("callback handler panic (middleware)",
								zap.Any("panic", rec),
								zap.String("namespace", next.Namespace()),
							)
						}
						err = fmt.Errorf("%w: handler panic: %v", core.ErrInternal, rec)
						if metrics != nil {
							metrics.RecordCallback("handler_panic", time.Since(start), err)
						}
					}
				}()
				err = next.HandleCallback(ctx)
				return err
			},
		}
	}
}

// RateLimitMiddleware is a stub for the future pipeline; current rate limiting stays inline in Dispatch.
func RateLimitMiddleware() Middleware {
	return func(next Handler) Handler { return next }
}

// TimeoutMiddleware returns a Middleware that enforces per-handler timeout via context.WithTimeout.
func TimeoutMiddleware(timeout time.Duration) Middleware {
	return func(next Handler) Handler {
		return handlerFunc{
			ns: next.Namespace(),
			fn: func(ctx *CallbackContext) error {
				if timeout <= 0 {
					return next.HandleCallback(ctx)
				}
				hCtx, cancel := context.WithTimeout(ctx.Ctx, timeout)
				defer cancel()
				prev := ctx.Ctx
				ctx.Ctx = hCtx
				defer func() { ctx.Ctx = prev }()
				return next.HandleCallback(ctx)
			},
		}
	}
}

// handlerFunc is a lightweight Handler adapter for middleware construction.
type handlerFunc struct {
	ns string
	fn func(*CallbackContext) error
}

func (h handlerFunc) Namespace() string { return h.ns }
func (h handlerFunc) HandleCallback(ctx *CallbackContext) error {
	return h.fn(ctx)
}
