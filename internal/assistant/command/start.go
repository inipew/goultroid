package command

import (
	"time"

	"github.com/inipew/goultroid/internal/assistant/menu"
)

// RegisterStart attaches the /start command handler to the Router.
func RegisterStart(r *Router, usernameProvider func() string, uptimeProvider func() time.Duration, renderer menu.RendererFunc) {
	if r == nil {
		return
	}
	r.Register("/start", func(c *Context) error {
		username := "GoUltroidBot"
		if usernameProvider != nil {
			username = usernameProvider()
		}
		var uptime time.Duration
		if uptimeProvider != nil {
			uptime = uptimeProvider()
		}
		screen := menu.BuildStartScreen(username, uptime)
		text, markup := renderer(screen)
		_, err := c.Reply(text, markup)
		return err
	})
}
