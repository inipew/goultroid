package command

import (
	appPing "github.com/inipew/goultroid/internal/application/ping"
	"github.com/inipew/goultroid/internal/assistant/interaction"
)

// PingResult records latency metrics for assistant ping commands.
type PingResult = appPing.Result

// RegisterPing attaches the /ping command handler to the Router.
func RegisterPing(r *Router) {
	if r == nil {
		return
	}
	r.Register("/ping", func(c *Context) error {
		uc := appPing.NewUseCase()
		var sentID int
		res, err := uc.Execute(func() error {
			sent, sErr := c.Reply("🏓 ...", nil)
			if sErr != nil {
				return sErr
			}
			if sent != nil {
				sentID = sent.ID
			}
			return nil
		})
		if err != nil {
			return err
		}
		if sentID > 0 {
			target := interaction.NewMessageTarget(c.Peer, sentID, 0, 0)
			return c.Interaction.Edit(c.Ctx, target, appPing.FormatResult(res.Latency), nil)
		}
		return nil
	})
}
