package command

import (
	"time"

	"github.com/inipew/goultroid/internal/assistant/menu"
)

// RuntimeStatus contains operational metrics for the assistant runtime.
type RuntimeStatus struct {
	Uptime      time.Duration
	BotUsername string
	Engine      string
	Goroutines  int
	GoVersion   string
}

// RegisterStatus attaches the /status command handler to the Router.
func RegisterStatus(r *Router, usernameProvider func() string, uptimeProvider func() time.Duration, renderer menu.RendererFunc, instanceStore menu.InstanceStore) {
	if r == nil {
		return
	}
	r.Register("/status", func(c *Context) error {
		username := "GoUltroidBot"
		if usernameProvider != nil {
			username = usernameProvider()
		}
		var uptime time.Duration
		if uptimeProvider != nil {
			uptime = uptimeProvider()
		}
		screen := menu.BuildStatusScreen(username, uptime, "GoUltroid (MTProto) v2")
		text, markup := renderer(screen)
		sent, err := c.Reply(text, markup)
		if err == nil && sent != nil && instanceStore != nil {
			chatID := extractChatIDFromInputPeer(c.Peer)
			if chatID == 0 {
				chatID = c.SenderID
			}
			instanceStore.Register(menu.MenuInstance{
				ChatID:    chatID,
				MessageID: sent.ID,
				Screen:    menu.ScreenIDStatus,
				OwnerID:   c.SenderID,
			})
		}
		return err
	})
}
