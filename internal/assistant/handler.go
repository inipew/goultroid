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

// CallbackOptions configures handler UX parameters.
func (h *Handler) CallbackOptions() callback.CallbackHandlerOptions {
	return callback.CallbackHandlerOptions{
		AutoAnswer: true,
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
		return ctx.Edit(text, markup)

	case "status":
		screen := RenderStatusScreen(username, h.startTime)
		text, markup := render.ToTelegram(screen)
		return ctx.Edit(text, markup)

	case "ping":
		_ = ctx.Answer("🏓 Pong!", true)
		screen := RenderStartMenu(username, h.startTime)
		text, markup := render.ToTelegram(screen)
		return ctx.Edit(text, markup)

	case "close":
		return ctx.Delete()

	default:
		return ctx.Answer("Unknown action", false)
	}
}
