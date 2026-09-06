package core

import (
	"errors"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// CommandRateLimiter is the minimal dependency the core executor needs to
// enforce a global command budget without importing a concrete rate-limit
// implementation and creating a package cycle.
type CommandRateLimiter interface {
	Allow(key string) bool
}

type CommandExecutor struct {
	logger          *zap.Logger
	cooldown        *CooldownTracker
	defaultTimeout  time.Duration
	metrics         MetricsCollector
	rateLimiter     CommandRateLimiter
}

func NewCommandExecutor(logger *zap.Logger, cooldown *CooldownTracker, defaultTimeout time.Duration) *CommandExecutor {
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}
	return &CommandExecutor{
		logger: logger,
		cooldown: cooldown,
		defaultTimeout: defaultTimeout,
	}
}

func (e *CommandExecutor) SetMetrics(metrics MetricsCollector) { e.metrics = metrics }
func (e *CommandExecutor) Metrics() MetricsCollector { return e.metrics }
func (e *CommandExecutor) SetRateLimiter(limiter CommandRateLimiter) { e.rateLimiter = limiter }
func (e *CommandExecutor) RateLimiter() CommandRateLimiter { return e.rateLimiter }

func (e *CommandExecutor) Execute(ctx *Context, cmd Command) error {
	if ctx == nil {
		return ErrInternal
	}
	if ctx.Message != nil && ctx.Chat != nil && ConsumeMessageHandled(ctx.Chat.ID, ctx.Message.ID) {
		return nil
	}

	if e.rateLimiter != nil {
		key := strconv.FormatInt(ctx.SenderID(), 10)
		if ctx.SenderID() <= 0 {
			key = "anonymous"
		}
		if !e.rateLimiter.Allow(key) {
			return ErrRateLimited
		}
	}

	chain := NewChain(
		RecoveryMiddleware(e.logger),
		CorrelationMiddleware(e.logger),
		LoggingMiddleware(e.logger),
		PermissionMiddleware(cmd),
		FilterMiddleware(cmd),
		CooldownMiddleware(cmd, e.cooldown),
		TimeoutMiddleware(cmd, e.defaultTimeout),
	)
	start := time.Now()
	err := chain.Then(cmd.Handler)(ctx)
	if e.metrics != nil {
		e.metrics.RecordCommand(cmd.Name, time.Since(start), err)
	}
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrCooldownActive) || errors.Is(err, ErrRateLimited) {
			return err
		}
		if errors.Is(err, ErrGroupOnly) || errors.Is(err, ErrPrivateOnly) || errors.Is(err, ErrReplyRequired) {
			_ = ctx.Reply("⚠️ " + err.Error())
			return err
		}
		if e.logger != nil {
			e.logger.Error("command failed", zap.String("correlation_id", ctx.CorrelationID), zap.String("command", cmd.Name), zap.Error(err))
		}
	}
	return err
}
