package core

// FilterMiddlewareForSource applies contextual command restrictions according
// to the explicit execution source. Interactive outgoing userbot messages keep
// their historical compatibility exception; non-interactive sources never do.
func FilterMiddlewareForSource(cmd Command, source ExecutionSource) Middleware {
	return func(next CommandHandler) CommandHandler {
		return func(ctx *Context) error {
			if source == ExecutionInteractive && ctx.Message != nil && ctx.Message.IsOutgoing {
				if cmd.ReplyOnly && ctx.Message.ReplyToID == 0 {
					return ErrReplyRequired
				}
				return next(ctx)
			}
			if cmd.GroupOnly && !ctx.IsGroup() {
				return ErrGroupOnly
			}
			if cmd.PrivateOnly && !ctx.IsPrivate() {
				return ErrPrivateOnly
			}
			if cmd.ReplyOnly && (ctx.Message == nil || ctx.Message.ReplyToID == 0) {
				return ErrReplyRequired
			}
			return next(ctx)
		}
	}
}
