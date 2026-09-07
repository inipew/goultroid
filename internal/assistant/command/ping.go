package command

import (
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/assistant/interaction"
)

// PingResult records latency metrics for assistant ping commands.
type PingResult struct {
	Latency time.Duration
}

// RegisterPing attaches the /ping command handler to the Router.
func RegisterPing(r *Router) {
	if r == nil {
		return
	}
	r.Register("/ping", func(c *Context) error {
		start := time.Now()
		sent, err := c.Reply("🏓 ...", nil)
		if err != nil {
			return err
		}
		latency := time.Since(start).Milliseconds()
		if sent != nil {
			target := interaction.NewMessageTarget(c.Peer, sent.ID, 0, 0)
			return c.Interaction.Edit(c.Ctx, target, fmt.Sprintf("🏓 <b>Pong!</b>\n⚡ <b>Latency:</b> <code>%d ms</code>", latency), nil)
		}
		return nil
	})
}
