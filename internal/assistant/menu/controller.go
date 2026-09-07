package menu

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
)

// RendererFunc renders a Screen into text and markup.
type RendererFunc func(screen *Screen) (string, tg.ReplyMarkupClass)

// Controller manages the generation and transition between assistant interactive screens.
type Controller struct {
	renderer RendererFunc
}

// NewController creates a menu Controller.
func NewController(renderer RendererFunc) *Controller {
	return &Controller{
		renderer: renderer,
	}
}

// BuildStartScreen constructs the main assistant dashboard screen.
func BuildStartScreen(botUsername string, uptime time.Duration) *Screen {
	if botUsername == "" {
		botUsername = "GoUltroidBot"
	}
	uptimeStr := uptime.Truncate(time.Second).String()
	body := fmt.Sprintf(
		"👋 <b>Welcome to GoUltroid Assistant!</b>\n\n"+
			"• <b>Bot:</b> @%s\n"+
			"• <b>Uptime:</b> %s\n"+
			"• <b>Status:</b> 🟢 Online & Active\n\n"+
			"<i>Select an option below to manage and interact with your userbot:</i>",
		botUsername, uptimeStr,
	)

	screen := NewScreen(ScreenIDStart, "🤖 GoUltroid Assistant", body)
	screen.AddRow(
		NewButton("⚙️ Settings", "a1:assistant:settings"),
		NewButton("📚 Help / Modules", "a1:assistant:help"),
	)
	screen.AddRow(
		NewButton("📊 System Status", "a1:assistant:status"),
		NewButton("🏓 Ping", "a1:assistant:ping"),
	)
	screen.AddRow(
		NewButton("🔒 Close Menu", "a1:assistant:close"),
	)
	return screen
}

// BuildSettingsScreen constructs the settings navigation screen.
func BuildSettingsScreen(botUsername string) *Screen {
	body := "Settings storage and mutation are managed by the core settings subsystem.\n\n" +
		"Use userbot settings commands or configure your userbot settings."

	screen := NewScreen(ScreenIDSettings, "⚙️ Assistant Settings", body)
	screen.AddRow(
		NewButton("« Back to Menu", "a1:assistant:start"),
	)
	return screen
}

// BuildHelpScreen constructs the help and commands index screen.
func BuildHelpScreen(botUsername string) *Screen {
	body := "/start — open the interactive dashboard\n" +
		"/help — show this help overview\n" +
		"/ping — check responsiveness\n" +
		"/status — view system status\n" +
		"/alive — check assistant status"

	screen := NewScreen(ScreenIDHelp, "📚 Help / Modules", body)
	screen.AddRow(
		NewButton("« Back to Menu", "a1:assistant:start"),
	)
	return screen
}

// BuildStatusScreen constructs the system diagnostics screen.
func BuildStatusScreen(botUsername string, uptime time.Duration, engine string) *Screen {
	if botUsername == "" {
		botUsername = "GoUltroidBot"
	}
	if engine == "" {
		engine = "GoUltroid (MTProto) v2"
	}
	uptimeStr := uptime.Truncate(time.Second).String()
	body := fmt.Sprintf(
		"• <b>Assistant Bot:</b> @%s\n"+
			"• <b>Uptime:</b> %s\n"+
			"• <b>Engine:</b> %s\n"+
			"• <b>Callback Engine:</b> Active (Pipeline v2)\n"+
			"• <b>Status:</b> All systems operational.\n",
		botUsername, uptimeStr, engine,
	)

	screen := NewScreen(ScreenIDStatus, "📊 System Status", body)
	screen.AddRow(
		NewButton("🔄 Refresh", "a1:assistant:status"),
		NewButton("« Back to Menu", "a1:assistant:start"),
	)
	return screen
}

// AttachRoutes registers all standard assistant menu actions into the given Router.
func (c *Controller) AttachRoutes(r *callback.Router, getUsername func() string, getStartTime func() time.Time) {
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

	// 1. Start Menu
	r.Register("assistant", "start", func(ctx context.Context, tx *callback.Transaction) error {
		_ = tx.Answer(ctx, "", false)
		screen := BuildStartScreen(username(), uptime())
		text, markup := c.renderer(screen)
		return tx.Edit(ctx, text, markup)
	})

	// 2. Settings Menu
	r.Register("assistant", "settings", func(ctx context.Context, tx *callback.Transaction) error {
		_ = tx.Answer(ctx, "", false)
		screen := BuildSettingsScreen(username())
		text, markup := c.renderer(screen)
		return tx.Edit(ctx, text, markup)
	})

	// 3. Help Menu
	r.Register("assistant", "help", func(ctx context.Context, tx *callback.Transaction) error {
		_ = tx.Answer(ctx, "", false)
		screen := BuildHelpScreen(username())
		text, markup := c.renderer(screen)
		return tx.Edit(ctx, text, markup)
	})

	// 4. Status Menu
	r.Register("assistant", "status", func(ctx context.Context, tx *callback.Transaction) error {
		_ = tx.Answer(ctx, "", false)
		screen := BuildStatusScreen(username(), uptime(), "GoUltroid (MTProto) v2")
		text, markup := c.renderer(screen)
		return tx.Edit(ctx, text, markup)
	})

	// 5. Ping Action (toast popup only, screen untouched)
	r.Register("assistant", "ping", func(ctx context.Context, tx *callback.Transaction) error {
		return tx.Answer(ctx, "🏓 Pong!", true)
	})

	// 6. Close Action (idempotent delete)
	r.Register("assistant", "close", func(ctx context.Context, tx *callback.Transaction) error {
		_ = tx.Answer(ctx, "Menu closed", false)
		return tx.Delete(ctx)
	})
}
