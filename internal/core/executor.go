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
	return &CommandExecutor{
		logger:         logger,
		cooldown:       cooldown,
		defaultTimeout: defaultTimeout,
	}
}

func (e *CommandExecutor) SetMetrics(metrics MetricsCollector) { e.metrics = metrics }
func (e *CommandExecutor) Metrics() MetricsCollector            { return e.metrics }
func (e *CommandExecutor) SetRateLimiter(limiter CommandRateLimiter) {
	e.rateLimiter = limiter
}
func (e *CommandExecutor) RateLimiter() CommandRateLimiter { return e.rateLimiter }

// Execute preserves the legacy Context-based entry point for interactive
// callers. New non-interactive callers should use ExecuteExecution so the
// source of execution is explicit rather than inferred from Message fields.
func (e *CommandExecutor) Execute(ctx *Context, cmd Command) error {
	if ctx == nil {
		return ErrInternal
	}
	return e.execute(ctx, cmd, ExecutionInteractive)
}

// ExecuteExecution materializes the canonical execution envelope into the
// command Context and then runs the same middleware/handler path used by
// interactive commands.
func (e *CommandExecutor) ExecuteExecution(exec CommandExecution, cmd Command, svc TelegramServicer) error {
	if exec.Ctx == nil {
		exec.Ctx = timeBackground()
	}
	ctx := &Context{
		Ctx:            exec.Ctx,
		CorrelationID:  exec.CorrelationID,
		Command:        exec.Command,
		Args:           append([]string(nil), exec.Args...),
		RawArgs:        exec.RawArgs,
		Message:        exec.TriggerMessage,
		Chat:           exec.Chat,
		Sender:         principalUser(exec.Principal),
		Principal:      exec.Principal,
		Svc:            svc,
		PeerID:         exec.PeerID,
	}
	if ctx.Command == "" {
		ctx.Command = cmd.Name
	}
	return e.execute(ctx, cmd, exec.Source)
}

func (e *CommandExecutor) execute(ctx *Context, cmd Command, source ExecutionSource) error {
	if ctx == nil {
		return ErrInternal
	}
	if ctx.Ctx == nil {
		ctx.Ctx = timeBackground()
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
			e.logger.Error("command failed", zap.String("correlation_id", ctx.CorrelationID), zap.String("command", cmd.Name), zap.String("source", source.String()), zap.Error(err))
		}
	}
	return err
}

// principalUser adapts the canonical principal to the legacy Context sender.
// Principal is the authority identity; Sender remains a compatibility view for
// existing command implementations.
func principalUser(p *Principal) *User {
	if p == nil || p.ID <= 0 {
		return nil
	}
	return &User{ID: p.ID}
}

// timeBackground is isolated to keep the execution constructor simple and
// avoid exposing a mutable nil context to middleware.
func timeBackground() interface{ Done() <-chan struct{} } {
	return nil
}
