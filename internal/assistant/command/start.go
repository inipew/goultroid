package command

import (
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/ui"
)

// RegisterStart attaches the /start command handler to the Router.
func RegisterStart(r *Router, usernameProvider func() string, uptimeProvider func() time.Duration, renderer menu.RendererFunc, instanceStore menu.InstanceStore) {
	if r == nil {
		return
	}
	r.Register("/start", func(c *Context) error {
		// Deep link token processing: /start <token>
		if len(c.Args) > 0 && c.Args[0] != "" && r.DeepLinks() != nil && r.Presentation() != nil {
			token := c.Args[0]
			chatID := extractChatIDFromInputPeer(c.Peer)
			if chatID == 0 {
				chatID = c.SenderID
			}

			actor := execution.NewActor(c.SenderID, chatID, false, false)
			if r.OwnerID() != 0 && c.SenderID == r.OwnerID() {
				actor.IsOwner = true
			}

			claim, err := r.DeepLinks().Consume(c.Ctx, token, actor)
			if err != nil {
				_, replyErr := c.Reply("ℹ️ <i>This link has expired, was already used, or is not intended for your account. Please request a new link.</i>", nil)
				return replyErr
			}

			buildRes, err := r.Presentation().Build(c.Ctx, presentation.BuildRequest{
				Key:           claim.Screen,
				Actor:         actor,
				Source:        execution.SourceAssistant,
				ChatType:      presentation.ChatTypePrivate,
				CorrelationID: fmt.Sprintf("start-%d-%d", c.SenderID, time.Now().UnixNano()),
				Input:         claim.Payload,
			})
			if err != nil {
				safeMsg := "⚠️ <i>Unable to display screen. Please try again later.</i>"
				if errors.Is(err, presentation.ErrAccessDenied) || errors.Is(err, presentation.ErrPrivateRequired) {
					safeMsg = "⚠️ <i>Access denied to this screen.</i>"
				} else if errors.Is(err, presentation.ErrScreenNotFound) {
					safeMsg = "⚠️ <i>The requested screen is no longer available.</i>"
				}
				_, replyErr := c.Reply(safeMsg, nil)
				return replyErr
			}

			var text string
			var markup tg.ReplyMarkupClass
			if renderer != nil {
				text, markup = renderer(buildRes.Screen)
			} else if buildRes.Screen != nil {
				text = buildRes.Screen.Text()
			}

			sent, err := c.Reply(text, markup)
			if err == nil && sent != nil && instanceStore != nil {
				instanceStore.Register(menu.MenuInstance{
					ID:        fmt.Sprintf("menu:%d:%d", chatID, sent.ID),
					ChatID:    chatID,
					MessageID: sent.ID,
					Screen:    claim.Screen.String(),
					OwnerID:   c.SenderID,
				})
			}
			return err
		}

		// Standard start screen
		chatID := extractChatIDFromInputPeer(c.Peer)
		if chatID == 0 {
			chatID = c.SenderID
		}
		actor := execution.NewActor(c.SenderID, chatID, false, false)
		if r.OwnerID() != 0 && c.SenderID == r.OwnerID() {
			actor.IsOwner = true
		}

		var screen *ui.Screen
		if r.Presentation() != nil {
			buildRes, bErr := r.Presentation().Build(c.Ctx, presentation.BuildRequest{
				Key:           presentation.ScreenKey{Namespace: "core", Name: "start", Version: 1},
				Actor:         actor,
				Source:        execution.SourceAssistant,
				ChatType:      presentation.ChatTypePrivate,
				CorrelationID: fmt.Sprintf("start-std-%d-%d", c.SenderID, time.Now().UnixNano()),
			})
			if bErr == nil {
				screen = buildRes.Screen
			}
		}

		if screen == nil {
			username := "GoUltroidBot"
			if usernameProvider != nil {
				username = usernameProvider()
			}
			var uptime time.Duration
			if uptimeProvider != nil {
				uptime = uptimeProvider()
			}
			screen = menu.BuildStartScreen(username, uptime)
		}
		var text string
		var markup tg.ReplyMarkupClass
		if renderer != nil {
			text, markup = renderer(screen)
		} else if screen != nil {
			text = screen.Text()
		}
		sent, err := c.Reply(text, markup)
		if err == nil && sent != nil && instanceStore != nil {
			chatID := extractChatIDFromInputPeer(c.Peer)
			if chatID == 0 {
				chatID = c.SenderID
			}
			instanceStore.Register(menu.MenuInstance{
				ID:        fmt.Sprintf("menu:%d:%d", chatID, sent.ID),
				ChatID:    chatID,
				MessageID: sent.ID,
				Screen:    menu.ScreenIDStart,
				OwnerID:   c.SenderID,
			})
		}
		return err
	})
}

// AttachDefaultCommands registers /start into the command Router.
func AttachDefaultCommands(r *Router, getUsername func() string, getStartTime func() time.Time, renderer menu.RendererFunc) {
	AttachDefaultCommandsWithStore(r, getUsername, getStartTime, renderer, nil)
}

// AttachDefaultCommandsWithStore registers /start into the command Router with optional session instance store.
func AttachDefaultCommandsWithStore(r *Router, getUsername func() string, getStartTime func() time.Time, renderer menu.RendererFunc, store menu.InstanceStore) {
	if r == nil {
		return
	}

	uptime := func() time.Duration {
		if getStartTime != nil {
			return time.Since(getStartTime())
		}
		return 0
	}

	username := func() string {
		if getUsername != nil {
			return getUsername()
		}
		return "GoUltroidBot"
	}

	RegisterStart(r, username, uptime, renderer, store)
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
