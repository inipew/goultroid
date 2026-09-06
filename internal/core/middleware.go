package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"go.uber.org/zap"
)

// Middleware wraps a CommandHandler, providing pre/post processing hooks.
type Middleware func(next CommandHandler) CommandHandler

// Chain compiles a slice of Middlewares into a single wrapper.
type Chain struct {
	middlewares []Middleware
}

// NewChain initializes a new Middleware Chain.
func NewChain(middlewares ...Middleware) *Chain {
	return &Chain{middlewares: middlewares}
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
							zap.String("correlation_id", ctx.CorrelationID),
							zap.String("command", ctx.Command),
							zap.Any("recover", r),
							zap.String("stack", stack),
						)
					}
					err = fmt.Errorf("%w: panic in command %s: %v", ErrInternal, ctx.Command, r)
				}
			}()
			return next(ctx)
		}
	}
}

// CorrelationMiddleware ensures every command execution context has a unique correlation ID.
// If the context does not have one, a new ID is generated using timestamp and random hex bytes.
func CorrelationMiddleware(logger *zap.Logger) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			if ctx.CorrelationID == "" {
				b := make([]byte, 4)
				_, _ = rand.Read(b)
				ctx.CorrelationID = fmt.Sprintf("req-%d-%x", time.Now().UnixMilli(), b)
			}
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
					zap.String("correlation_id", ctx.CorrelationID),
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
					var rle *RateLimitError
					if errors.As(err, &rle) {
						logger.Warn("command hit telegram rate limit",
							zap.String("correlation_id", ctx.CorrelationID),
							zap.String("command", ctx.Command),
							zap.Duration("flood_wait", rle.Wait),
							zap.Duration("duration", duration),
							zap.Error(err),
						)
					} else {
						logger.Warn("command executed with error",
							zap.String("correlation_id", ctx.CorrelationID),
							zap.String("command", ctx.Command),
							zap.Duration("duration", duration),
							zap.Error(err),
						)
					}
				} else {
					logger.Debug("command executed successfully",
						zap.String("correlation_id", ctx.CorrelationID),
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
// If cmd.Timeout > 0, it is used; otherwise, defaultTimeout is used.
func TimeoutMiddleware(cmd Command, defaultTimeout time.Duration) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			timeout := defaultTimeout
			if cmd.Timeout > 0 {
				timeout = cmd.Timeout
			}
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
// Security-sensitive commands fail closed when the permission provider is unavailable.
func PermissionMiddleware(cmd Command) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			if cmd.Permission != PermissionEveryone {
				var userID int64
				if ctx.Sender != nil {
					userID = ctx.Sender.ID
				}
				if ctx.Perms == nil || !ctx.Perms.CanRun(userID, cmd) {
					return ErrPermissionDenied
				}
			}
			return next(ctx)
		}
	}
}

// FilterMiddleware validates contextual requirements (GroupOnly, PrivateOnly, ReplyOnly).
// For outgoing messages (userbot owner commands), GroupOnly and PrivateOnly are bypassed
// because Telegram update delivery for outgoing messages may represent PeerID as PeerUser
// or the owner may be testing commands in direct chats/Saved Messages.
// The Telegram API RPC calls enforce actual contextual constraints and return descriptive errors if invalid.
func FilterMiddleware(cmd Command) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			isOutgoing := ctx.Message != nil && ctx.Message.IsOutgoing

			if !isOutgoing {
				if cmd.GroupOnly && !ctx.IsGroup() {
					return ErrGroupOnly
				}
				if cmd.PrivateOnly && !ctx.IsPrivate() {
					return ErrPrivateOnly
				}
			}

			if cmd.ReplyOnly && (ctx.Message == nil || ctx.Message.ReplyToID == 0) {
				return ErrReplyRequired
			}
			return next(ctx)
		}
	}
}

// CooldownMiddleware prevents command spamming by enforcing rate limits per user.
func CooldownMiddleware(cmd Command, tracker *CooldownTracker) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			if cmd.Cooldown <= 0 || tracker == nil {
				return next(ctx)
			}
			if ctx.IsOwner() {
				return next(ctx)
			}

			var userID int64
			if ctx.Sender != nil {
				userID = ctx.Sender.ID
			}
			if remaining, ok := tracker.CheckAndRecord(userID, cmd.Name, cmd.Cooldown); !ok {
				return fmt.Errorf("%w: wait %s", ErrCooldownActive, remaining.Round(time.Millisecond))
			}
			return next(ctx)
		}
	}
}
