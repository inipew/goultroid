package callback

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// Middleware wraps handler-local execution concerns. Admission concerns such as
// protocol validation and rate limiting are owned by Router.Prepare.
type Middleware func(Handler) Handler

// Chain builds a Handler chain from middlewares. Last middleware wraps the final handler first.
func Chain(final Handler, mws ...Middleware) Handler {
	h := final
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// RecoverMiddleware recovers panics and classifies them for the router's
// single terminal metrics write. It must remain the outermost middleware.
func RecoverMiddleware(logger *zap.Logger) Middleware {
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
						err = fmt.Errorf("%w: %v", ErrHandlerPanic, rec)
					}
				}()
				return next.HandleCallback(ctx)
			},
		}
	}
}

// TimeoutMiddleware supplies a fallback handler deadline. If an upstream owner
// such as TaskEngine already installed an equal or tighter deadline, reuse it
// instead of allocating a second timer.
func TimeoutMiddleware(timeout time.Duration) Middleware {
	return func(next Handler) Handler {
		return handlerFunc{
			ns: next.Namespace(),
			fn: func(ctx *CallbackContext) error {
				if timeout <= 0 {
					return next.HandleCallback(ctx)
				}
				if deadline, ok := ctx.Ctx.Deadline(); ok {
					remaining := time.Until(deadline)
					if remaining <= 0 {
						return ctx.Ctx.Err()
					}
					if remaining <= timeout {
						return next.HandleCallback(ctx)
					}
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
