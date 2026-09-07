package command

import (
	"github.com/inipew/goultroid/internal/assistant/menu"
)

// RegisterHelp attaches the /help command handler to the Router.
func RegisterHelp(r *Router, usernameProvider func() string, renderer menu.RendererFunc) {
	if r == nil {
		return
	}
	r.Register("/help", func(c *Context) error {
		username := "GoUltroidBot"
		if usernameProvider != nil {
			username = usernameProvider()
		}
		screen := menu.BuildHelpScreen(username)
		text, markup := renderer(screen)
		_, err := c.Reply(text, markup)
		return err
	})
}
