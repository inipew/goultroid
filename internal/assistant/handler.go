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

// CallbackOptions disables automatic acknowledgement so each action owns its
// callback lifecycle. Ping can show a visible toast, while Close acknowledges
// before deleting the originating message.
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
		if err := ctx.Edit(text, markup); err != nil {
			return err
		}
		return ctx.Answer("", false)

	case "settings":
		screen := RenderSettingsScreen(username, h.startTime)
		text, markup := render.ToTelegram(screen)
		if err := ctx.Edit(text, markup); err != nil {
			return err
		}
		return ctx.Answer("", false)

	case "help":
		screen := RenderHelpScreen(username, h.startTime)
		text, markup := render.ToTelegram(screen)
		if err := ctx.Edit(text, markup); err != nil {
			return err
		}
		return ctx.Answer("", false)

	case "status":
		screen := RenderStatusScreen(username, h.startTime)
		text, markup := render.ToTelegram(screen)
		if err := ctx.Edit(text, markup); err != nil {
			return err
		}
		return ctx.Answer("", false)

	case "ping":
		// Ping is toast-only. The original menu is deliberately left untouched,
		// so every button remains usable after the callback.
		return ctx.Answer("🏓 Pong!", true)

	case "close":
		// Acknowledge first so Telegram clears the callback loading state even
		// though the originating message is about to disappear.
		if err := ctx.Answer("Menu closed", false); err != nil {
			return err
		}
		return ctx.Delete()

	default:
		return ctx.Answer("Unknown action", false)
	}
}
