package command

import (
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

// RegisterHelp attaches the /help command handler to the Router.
func RegisterHelp(r *Router, usernameProvider func() string, renderer menu.RendererFunc, instanceStore menu.InstanceStore) {
	if r == nil {
		return
	}
	r.Register("/help", func(c *Context) error {
		username := "GoUltroidBot"
		if usernameProvider != nil {
			username = usernameProvider()
		}
		var cmds []core.Command
		if r.unifiedRegistry != nil {
			cmds = r.unifiedRegistry.CommandsForSurface(execution.SourceAssistant)
		}
		screen := menu.BuildHelpScreenWithCommands(username, cmds)
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
				Screen:    menu.ScreenIDHelp,
				OwnerID:   c.SenderID,
			})
		}
		return err
	})
}
