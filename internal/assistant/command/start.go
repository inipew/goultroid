package command

import (
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/menu"
)

// RegisterStart attaches the /start command handler to the Router.
func RegisterStart(r *Router, usernameProvider func() string, uptimeProvider func() time.Duration, renderer menu.RendererFunc, instanceStore menu.InstanceStore) {
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
		sent, err := c.Reply(text, markup)
		if err == nil && sent != nil && instanceStore != nil {
			chatID := extractChatIDFromInputPeer(c.Peer)
			if chatID == 0 {
				chatID = c.SenderID
			}
			instanceStore.Register(menu.MenuInstance{
				ChatID:    chatID,
				MessageID: sent.ID,
				Screen:    menu.ScreenIDStart,
				OwnerID:   c.SenderID,
			})
		}
		return err
	})
}

func extractChatIDFromInputPeer(peer tg.InputPeerClass) int64 {
	if peer == nil {
		return 0
	}
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return p.UserID
	case *tg.InputPeerChat:
		return p.ChatID
	case *tg.InputPeerChannel:
		return p.ChannelID
	case *tg.InputPeerSelf:
		return 0
	default:
		return 0
	}
}
