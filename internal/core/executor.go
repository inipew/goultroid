package core

import (
	"context"
	"errors"
	"fmt"
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
	logger         *zap.Logger
	cooldown       *CooldownTracker
	defaultTimeout time.Duration
	metrics        MetricsCollector
	rateLimiter    CommandRateLimiter
}

func NewCommandExecutor(logger *zap.Logger, cooldown *CooldownTracker, defaultTimeout time.Duration) *CommandExecutor {
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}
	return &CommandExecutor{logger: logger, cooldown: cooldown, defaultTimeout: defaultTimeout}
}

func (e *CommandExecutor) SetMetrics(metrics MetricsCollector) { e.metrics = metrics }
func (e *CommandExecutor) Metrics() MetricsCollector            { return e.metrics }
func (e *CommandExecutor) SetRateLimiter(limiter CommandRateLimiter) {
	e.rateLimiter = limiter
}
func (e *CommandExecutor) RateLimiter() CommandRateLimiter { return e.rateLimiter }

// Execute preserves the legacy Context-based entry point. Scheduler callers
// that still provide a compatibility context are recognized as scheduled by
// their reserved correlation prefix; new callers should use ExecuteExecution.
func (e *CommandExecutor) Execute(ctx *Context, cmd Command) error {
	if ctx == nil {
		return ErrInternal
	}
	source := ctx.Source
	return e.execute(ctx, cmd, source)
}

// ExecuteExecution is the canonical entry point for scheduled, assistant,
// and system execution. It does not manufacture a Telegram trigger message.
func (e *CommandExecutor) ExecuteExecution(exec CommandExecution, cmd Command, svc TelegramServicer) error {
	if exec.Ctx == nil {
		exec.Ctx = context.Background()
	}
	if exec.Command == "" {
		exec.Command = cmd.Name
	}
	ctx := &Context{
		Ctx:            exec.Ctx,
		CorrelationID:  exec.CorrelationID,
		Source:         exec.Source,
		Command:        exec.Command,
		Args:           append([]string(nil), exec.Args...),
		RawArgs:        exec.RawArgs,
		Message:        exec.TriggerMessage,
		Chat:           exec.Chat,
		Sender:         exec.Sender,
		Principal:      exec.Principal,
		Perms:          exec.Perms,
		Svc:            svc,
		PeerID:         exec.PeerID,
	}
	return e.execute(ctx, cmd, exec.Source)
}

func (e *CommandExecutor) execute(ctx *Context, cmd Command, source ExecutionSource) error {
	if ctx == nil {
		return ErrInternal
	}
	if ctx.Ctx == nil {
		ctx.Ctx = context.Background()
	}
	ctx.Source = source
	if ctx.Message != nil && ctx.Chat != nil && ConsumeMessageHandled(ctx.Chat.ID, ctx.Message.ID) {
		return nil
	}

	if e.rateLimiter != nil {
		var key string
		switch source {
		case ExecutionScheduled:
			key = "sched:" + cmd.Name
		case ExecutionAssistant:
			if ctx.SenderID() > 0 {
				key = fmt.Sprintf("assistant:%d", ctx.SenderID())
			} else {
				key = "assistant:" + cmd.Name
			}
		case ExecutionAddon:
			key = "addon:" + cmd.Name
		case ExecutionSystem:
			key = "system:" + cmd.Name
		case ExecutionAutomation:
			key = "auto:" + cmd.Name
		default:
			if ctx.SenderID() > 0 {
				key = strconv.FormatInt(ctx.SenderID(), 10)
			} else {
				key = "anonymous"
			}
		}
		if !e.rateLimiter.Allow(key) {
			return ErrRateLimited
		}
	}

	chain := NewChain(
		RecoveryMiddleware(e.logger),
		CorrelationMiddleware(e.logger),
		LoggingMiddleware(e.logger),
		SurfaceMiddleware(cmd, source),
		PermissionMiddleware(cmd),
		FilterMiddlewareForSource(cmd, source),
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
			e.logger.Error("command failed",
				zap.String("correlation_id", ctx.CorrelationID),
				zap.String("command", cmd.Name),
				zap.String("source", source.String()),
				zap.Error(err),
			)
		}
	}
	return err
}
