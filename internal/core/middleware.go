package core

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"go.uber.org/zap"
)

// ErrPermissionDenied is returned when a user attempts to execute a command without sufficient permissions.
var ErrPermissionDenied = errors.New("permission denied")

// Middleware wraps a CommandHandler, providing pre/post processing hooks.
type Middleware func(next CommandHandler) CommandHandler

// Chain compiles a slice of Middlewares into a single wrapper.
type Chain struct {
	middlewares []Middleware
}

// NewChain initializes a new Middleware Chain.
func NewChain(middlewares ...Middleware) *Chain {
	return &Chain{
		middlewares: middlewares,
	}
}

// Then applies the middleware chain to the given handler.
// Middlewares are executed in the order they were provided (outermost first).
func (c *Chain) Then(handler CommandHandler) CommandHandler {
	if handler == nil {
		handler = func(ctx *Context) error { return nil }
	}
	for i := len(c.middlewares) - 1; i >= 0; i-- {
		handler = c.middlewares[i](handler)
	}
	return handler
}

// RecoveryMiddleware catches panics during command execution and prevents bot crashes.
func RecoveryMiddleware(logger *zap.Logger) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					stack := string(debug.Stack())
					if logger != nil {
						logger.Error("panic recovered in command handler",
							zap.String("command", ctx.Command),
							zap.Any("recover", r),
							zap.String("stack", stack),
						)
					}
					err = fmt.Errorf("panic in command %s: %v", ctx.Command, r)
				}
			}()
			return next(ctx)
		}
	}
}

// LoggingMiddleware logs the execution, duration, and status of every command.
func LoggingMiddleware(logger *zap.Logger) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			start := time.Now()
			var userID int64
			if ctx.Sender != nil {
				userID = ctx.Sender.ID
			}
			var chatID int64
			if ctx.Chat != nil {
				chatID = ctx.Chat.ID
			}

			if logger != nil {
				logger.Debug("executing command",
					zap.String("command", ctx.Command),
					zap.Int64("user_id", userID),
					zap.Int64("chat_id", chatID),
					zap.Strings("args", ctx.Args),
				)
			}

			err := next(ctx)
			duration := time.Since(start)

			if logger != nil {
				if err != nil {
					logger.Warn("command executed with error",
						zap.String("command", ctx.Command),
						zap.Duration("duration", duration),
						zap.Error(err),
					)
				} else {
					logger.Debug("command executed successfully",
						zap.String("command", ctx.Command),
						zap.Duration("duration", duration),
					)
				}
			}
			return err
		}
	}
}

// TimeoutMiddleware enforces a deadline on the command execution context.
func TimeoutMiddleware(timeout time.Duration) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			if timeout <= 0 {
				return next(ctx)
			}
			timeoutCtx, cancel := context.WithTimeout(ctx.Ctx, timeout)
			defer cancel()

			ctx.Ctx = timeoutCtx
			return next(ctx)
		}
	}
}

// PermissionMiddleware enforces that the sender has sufficient permission for the command.
func PermissionMiddleware(cmd Command) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			var userID int64
			if ctx.Sender != nil {
				userID = ctx.Sender.ID
			}

			if ctx.Perms != nil && !ctx.Perms.CanRun(userID, cmd) {
				return ErrPermissionDenied
			}

			return next(ctx)
		}
	}
}
