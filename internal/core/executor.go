package core

import (
	"errors"
	"time"

	"go.uber.org/zap"
)

type CommandExecutor struct {
	logger *zap.Logger
	cooldown *CooldownTracker
	defaultTimeout time.Duration
	metrics MetricsCollector
}

func NewCommandExecutor(logger *zap.Logger, cooldown *CooldownTracker, defaultTimeout time.Duration) *CommandExecutor {
	if defaultTimeout <= 0 { defaultTimeout = 30 * time.Second }
	return &CommandExecutor{logger: logger, cooldown: cooldown, defaultTimeout: defaultTimeout}
}
func (e *CommandExecutor) SetMetrics(metrics MetricsCollector) { e.metrics = metrics }
func (e *CommandExecutor) Metrics() MetricsCollector { return e.metrics }

func (e *CommandExecutor) Execute(ctx *Context, cmd Command) error {
	if ctx == nil { return ErrInternal }
	// Security interceptors run before the dispatcher launches the command
	// goroutine. Consume the marker here so a denied PM can never reach the
	// actual command handler, without coupling core to the PMPermit service.
	if ctx.Message != nil && ctx.Chat != nil && ConsumeMessageHandled(ctx.Chat.ID, ctx.Message.ID) {
		return nil
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
	if e.metrics != nil { e.metrics.RecordCommand(cmd.Name, time.Since(start), err) }
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrCooldownActive) { return err }
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
