package assistant

import (
	"time"

	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/ui/render"
)

// Handler processes callback queries for the "assistant" namespace.
type Handler struct {
	client    Client
	startTime time.Time
}

var (
	_ callback.Handler            = (*Handler)(nil)
	_ callback.HandlerWithOptions = (*Handler)(nil)
)

// NewHandler creates a new assistant callback Handler.
func NewHandler(client Client, startTime time.Time) *Handler {
	return &Handler{
		client:    client,
		startTime: startTime,
	}
}

// Namespace returns the callback namespace identifier.
func (h *Handler) Namespace() string {
	return "assistant"
}

// CallbackOptions disables automatic acknowledgement so every assistant action
// owns its callback lifecycle deterministically.
func (h *Handler) CallbackOptions() callback.CallbackHandlerOptions {
	return callback.CallbackHandlerOptions{
		AutoAnswer: false,
	}
}

// HandleCallback routes and processes assistant actions.
func (h *Handler) HandleCallback(ctx *callback.CallbackContext) error {
	username := ""
	if h.client != nil {
		username = h.client.Username()
	}

	switch ctx.Action {
	case "start":
		screen := RenderStartMenu(username, h.startTime)
		text, markup := render.ToTelegram(screen)
		if err := ctx.Answer("", false); err != nil {
			return err
		}
		return ctx.Edit(text, markup)

	case "settings":
		screen := RenderSettingsScreen(username, h.startTime)
		text, markup := render.ToTelegram(screen)
		if err := ctx.Answer("", false); err != nil {
			return err
		}
		return ctx.Edit(text, markup)

	case "help":
		screen := RenderHelpScreen(username, h.startTime)
		text, markup := render.ToTelegram(screen)
		if err := ctx.Answer("", false); err != nil {
			return err
		}
		return ctx.Edit(text, markup)

	case "status":
		screen := RenderStatusScreen(username, h.startTime)
		text, markup := render.ToTelegram(screen)
		if err := ctx.Answer("", false); err != nil {
			return err
		}
		return ctx.Edit(text, markup)

	case "ping":
		// Ping is toast-only. The original menu is deliberately left untouched,
		// so every button remains usable after the callback.
		return ctx.Answer("🏓 Pong!", true)

	case "close":
		// Acknowledge first so Telegram clears the callback loading state before
		// deleting the originating menu message.
		if err := ctx.Answer("Menu closed", false); err != nil {
			return err
		}
		if botService, ok := ctx.Service.(*BotServiceAdapter); ok {
			return botService.deleteCallbackMessage(ctx.Ctx, ctx.Target.Peer, ctx.Target.MessageID)
		}
		return ctx.Delete()

	default:
		return ctx.Answer("Unknown action", false)
	}
}
