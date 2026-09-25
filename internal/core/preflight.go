package core

import "context"

// PreflightCommand applies the side-effect-free command admission checks that
// are safe to run before TaskEngine admission. Stateful rate limiting,
// cooldown accounting, timeout ownership, recovery, logging, and metrics stay
// in CommandExecutor so they execute exactly once with the physical task.
func PreflightCommand(ctx *Context, cmd Command, source ExecutionSource) error {
	if ctx == nil {
		return NormalizeExecutionError(ErrInternal)
	}
	if ctx.Ctx == nil {
		ctx.Ctx = context.Background()
	}
	ctx.Source = source
	chain := NewChain(
		SurfaceMiddleware(cmd, source),
		InvocationMiddleware(cmd, source),
		PermissionMiddlewareForSource(cmd, source),
		FilterMiddlewareForSource(cmd, source),
	)
	return NormalizeExecutionError(chain.Then(nil)(ctx))
}
