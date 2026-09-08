package execution

import "fmt"

// Policy describes transport and authorization constraints that must be
// satisfied before an execution is allowed to reach feature code.
// It deliberately depends only on execution primitives so policy remains
// independent from core.Command and cannot introduce an import cycle.
type Policy struct {
	Surfaces      SurfaceMask
	RequireOwner  bool
	RequireSudo   bool
	GroupOnly     bool
	PrivateOnly   bool
	ReplyRequired bool
}

// Validate checks transport, actor, and message-shape constraints in a fixed
// order. A zero surface mask means all supported surfaces for policy objects.
func (p Policy) Validate(ctx *ExecutionContext) error {
	if ctx == nil {
		return fmt.Errorf("execution: nil context")
	}

	mask := p.Surfaces
	if mask == 0 {
		mask = SurfaceAll
	}
	if !mask.Supports(ctx.Source) {
		return fmt.Errorf("execution: source %s is not allowed", ctx.Source)
	}
	if !ctx.Actor.CanExecute(p.RequireOwner, p.RequireSudo) {
		return fmt.Errorf("execution: actor %d is not authorized", ctx.Actor.UserID)
	}

	if p.GroupOnly && p.PrivateOnly {
		return fmt.Errorf("execution: group-only and private-only policies conflict")
	}
	if p.GroupOnly && ctx.ChatID == 0 {
		return fmt.Errorf("execution: group-only policy requires a chat")
	}
	if p.PrivateOnly && ctx.ChatID == 0 {
		return fmt.Errorf("execution: private-only policy requires a chat")
	}
	if p.ReplyRequired && ctx.MessageID == 0 {
		return fmt.Errorf("execution: reply-required policy requires a message")
	}
	return nil
}
