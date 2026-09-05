package core

import (
	"errors"
	"time"

	"go.uber.org/zap"
)

// CommandExecutor manages the standardized command execution pipeline.
// Both Dispatcher (incoming Telegram updates) and Scheduler (scheduled jobs) execute commands
// through this executor to guarantee identical middleware enforcement.
type CommandExecutor struct {
	logger         *zap.Logger
	cooldown       *CooldownTracker
	defaultTimeout time.Duration
}

// NewCommandExecutor creates a new CommandExecutor instance.
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

// Execute executes a command for a given context through the unified middleware chain:
// Recovery -> Logging -> Permission -> Filter -> Cooldown -> Timeout -> Handler.
func (e *CommandExecutor) Execute(ctx *Context, cmd Command) error {
	chain := NewChain(
		RecoveryMiddleware(e.logger),
		LoggingMiddleware(e.logger),
		PermissionMiddleware(cmd),
		FilterMiddleware(cmd),
		CooldownMiddleware(cmd, e.cooldown),
		TimeoutMiddleware(cmd, e.defaultTimeout),
	)

	handler := chain.Then(cmd.Handler)
	err := handler(ctx)
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrCooldownActive) {
			return err
		}
		if errors.Is(err, ErrGroupOnly) || errors.Is(err, ErrPrivateOnly) || errors.Is(err, ErrReplyRequired) {
			_ = ctx.Reply("⚠️ " + err.Error())
			return err
		}
		if e.logger != nil {
			e.logger.Error("command failed",
				zap.String("command", cmd.Name),
				zap.Error(err),
			)
		}
	}
	return err
}
