package menu

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	appStatus "github.com/inipew/goultroid/internal/application/status"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

// RendererFunc renders a Screen into text and markup.
type RendererFunc func(screen *Screen) (string, tg.ReplyMarkupClass)

// CommandSource defines capability to query commands for a surface.
type CommandSource interface {
	CommandsForSurface(source execution.Source) []core.Command
}

// Controller manages the generation and transition between assistant interactive screens.
type Controller struct {
	renderer  RendererFunc
	instances InstanceStore
	cmdSource CommandSource
}

// NewController creates a menu Controller with instance tracking.
func NewController(renderer RendererFunc) *Controller {
	return &Controller{
		renderer:  renderer,
		instances: NewMemoryInstanceStore(DefaultMenuTTL),
	}
}

// SetCommandSource configures the command source for dynamic help rendering.
func (c *Controller) SetCommandSource(cs CommandSource) {
	c.cmdSource = cs
}

// Instances returns the underlying instance store.
func (c *Controller) Instances() InstanceStore {
	return c.instances
}

// BuildStartScreen constructs the main assistant dashboard screen.
func BuildStartScreen(botUsername string, uptime time.Duration) *Screen {
	if botUsername == "" {
		botUsername = "GoUltroidBot"
	}
	uptimeStr := appStatus.FormatDuration(uptime)
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
	body := "⚙️ <b>GoUltroid Settings Subsystem</b>\n\n" +
		"Manage your userbot configurations, privacy, security, and plugins.\n\n" +
		"• Use <code>.set</code> commands in chat, or open the interactive dashboard below."

	screen := NewScreen(ScreenIDSettings, "⚙️ Assistant Settings", body)
	screen.AddRow(
		NewButton("📂 Settings Dashboard", "v1:settings:nav:home"),
	)
	screen.AddRow(
		NewButton("« Back to Menu", "a1:assistant:start"),
	)
	return screen
}

// BuildHelpScreen constructs the help and commands index screen with defaults.
func BuildHelpScreen(botUsername string) *Screen {
	return BuildHelpScreenWithCommands(botUsername, nil)
}

// BuildHelpScreenWithCommands constructs the help screen with dynamic command listings.
func BuildHelpScreenWithCommands(botUsername string, cmds []core.Command) *Screen {
	var body string
	if len(cmds) > 0 {
		var sb strings.Builder
		sb.WriteString("<b>GoUltroid Assistant Commands</b>\n\n")
		sortedCmds := make([]core.Command, len(cmds))
		copy(sortedCmds, cmds)
		sort.Slice(sortedCmds, func(i, j int) bool {
			return sortedCmds[i].Name < sortedCmds[j].Name
		})
		for _, cmd := range sortedCmds {
			desc := cmd.Description
			if desc == "" {
				desc = "No description"
			}
			sb.WriteString(fmt.Sprintf("/%s — %s\n", cmd.Name, desc))
		}
		sb.WriteString("\n<i>Browse all installed userbot modules below:</i>")
		body = sb.String()
	} else {
		body = "<b>GoUltroid Assistant Commands</b>\n\n" +
			"/start — open the interactive dashboard\n" +
			"/help — show this help overview\n" +
			"/ping — check responsiveness\n" +
			"/status — view system status\n" +
			"/alive — check assistant status\n\n" +
			"<i>Browse all installed userbot modules below:</i>"
	}

	screen := NewScreen(ScreenIDHelp, "📚 Help / Modules", body)
	screen.AddRow(
		NewButton("📚 Browse All Modules", "v1:help:cat:noop"),
	)
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
	uptimeStr := appStatus.FormatDuration(uptime)
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

// lockInstance acquires the per-menu instance mutex.
func (c *Controller) lockInstance(tx *callback.Transaction) func() {
	if c.instances != nil && tx != nil {
		return c.instances.LockInstance(tx.Target.ChatID(), tx.Target.MessageID())
	}
	return func() {}
}

// validateSession verifies that a menu instance exists, is not expired,
// and enforces strict session ownership. An invalid/unknown actor is never
// treated as the owner of a menu instance.
func (c *Controller) validateSession(ctx context.Context, tx *callback.Transaction) (*MenuInstance, error) {
	if c.instances == nil {
		return nil, callback.ErrSessionExpired
	}
	if tx == nil || !tx.Target.IsValid() || tx.UserID == 0 {
		if tx != nil {
			_ = tx.Answer(ctx, "Session expired, send /start again", true)
		}
		return nil, callback.ErrSessionExpired
	}
	inst, ok := c.instances.Get(tx.Target.ChatID(), tx.Target.MessageID())
	if !ok || inst == nil {
		_ = tx.Answer(ctx, "Session expired, send /start again", true)
		return nil, callback.ErrSessionExpired
	}
	if inst.OwnerID == 0 || tx.UserID != inst.OwnerID {
		_ = tx.Answer(ctx, "⚠️ You do not own this menu!", true)
		return nil, callback.ErrUnauthorized
	}
	return inst, nil
}

// RegisterInstance registers a menu instance into the controller's session store.
func (c *Controller) RegisterInstance(inst MenuInstance) {
	if c.instances != nil {
		c.instances.Register(inst)
	}
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
		unlock := c.lockInstance(tx)
		defer unlock()

		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		if c.instances != nil {
			c.instances.UpdateScreen(tx.Target.ChatID(), tx.Target.MessageID(), ScreenIDStart)
		}
		screen := BuildStartScreen(username(), uptime())
		text, markup := c.renderer(screen)
		return tx.Edit(ctx, text, markup)
	})

	// 2. Settings Menu
	r.Register("assistant", "settings", func(ctx context.Context, tx *callback.Transaction) error {
		unlock := c.lockInstance(tx)
		defer unlock()

		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		if c.instances != nil {
			c.instances.UpdateScreen(tx.Target.ChatID(), tx.Target.MessageID(), ScreenIDSettings)
		}
		screen := BuildSettingsScreen(username())
		text, markup := c.renderer(screen)
		return tx.Edit(ctx, text, markup)
	})

	// 3. Help Menu
	r.Register("assistant", "help", func(ctx context.Context, tx *callback.Transaction) error {
		unlock := c.lockInstance(tx)
		defer unlock()

		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		if c.instances != nil {
			c.instances.UpdateScreen(tx.Target.ChatID(), tx.Target.MessageID(), ScreenIDHelp)
		}
		var cmds []core.Command
		if c.cmdSource != nil {
			cmds = c.cmdSource.CommandsForSurface(execution.SourceAssistant)
		}
		screen := BuildHelpScreenWithCommands(username(), cmds)
		text, markup := c.renderer(screen)
		return tx.Edit(ctx, text, markup)
	})

	// 4. Status Menu
	r.Register("assistant", "status", func(ctx context.Context, tx *callback.Transaction) error {
		unlock := c.lockInstance(tx)
		defer unlock()

		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		if c.instances != nil {
			c.instances.UpdateScreen(tx.Target.ChatID(), tx.Target.MessageID(), ScreenIDStatus)
		}
		screen := BuildStatusScreen(username(), uptime(), "GoUltroid (MTProto) v2")
		text, markup := c.renderer(screen)
		return tx.Edit(ctx, text, markup)
	})

	// 5. Ping Action (toast popup only, screen untouched)
	r.Register("assistant", "ping", func(ctx context.Context, tx *callback.Transaction) error {
		unlock := c.lockInstance(tx)
		defer unlock()

		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		return tx.Answer(ctx, "🏓 Pong!", true)
	})

	// 6. Close Action (idempotent delete)
	r.Register("assistant", "close", func(ctx context.Context, tx *callback.Transaction) error {
		unlock := c.lockInstance(tx)
		defer unlock()

		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		_ = tx.Answer(ctx, "Menu closed", false)
		if c.instances != nil {
			c.instances.Invalidate(tx.Target.ChatID(), tx.Target.MessageID())
		}
		return tx.Delete(ctx)
	})
}
