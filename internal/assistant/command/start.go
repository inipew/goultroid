package command

import (
	"github.com/gotd/td/tg"
)

// NewUnavailableStartHandler returns the bounded recovery response used only
// when the a2 interaction foundation cannot construct the owner shell.
func NewUnavailableStartHandler() Handler {
	return func(c *Context) error {
		if c == nil {
			return nil
		}
		_, err := c.Reply("⚠️ Assistant interaction is temporarily unavailable. Please send /start again after the service recovers.", nil)
		return err
	}
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
