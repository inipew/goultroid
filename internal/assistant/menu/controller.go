package menu

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	appStatus "github.com/inipew/goultroid/internal/application/status"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/ui"
)

type RendererFunc func(screen *Screen) (string, tg.ReplyMarkupClass)
type CommandSource interface {
	CommandsForSurface(source execution.Source) []core.Command
}

type Controller struct {
	renderer  RendererFunc
	instances InstanceStore
	cmdSource CommandSource
	registry  *Registry
	pendingMu sync.Mutex
	pending   map[int64]pendingSettingInput
}

func NewController(renderer RendererFunc) *Controller {
	c := &Controller{
		renderer:  renderer,
		instances: NewMemoryInstanceStore(DefaultMenuTTL),
		registry:  NewRegistry(),
		pending:   make(map[int64]pendingSettingInput),
	}
	c.registerDefaultScreens()
	return c
}

func (c *Controller) SetCommandSource(cs CommandSource) { c.cmdSource = cs }
func (c *Controller) Instances() InstanceStore           { return c.instances }
func (c *Controller) Registry() *Registry                 { return c.registry }

type ScreenContext struct {
	Username string
	Uptime   time.Duration
	Engine   string
	Commands []core.Command
}

func (c *Controller) registerDefaultScreens() {
	c.registry.Register(ScreenIDStart, func(ctx ScreenContext) (*Screen, error) {
		return BuildStartScreenWithCommands(ctx.Username, ctx.Uptime, ctx.Commands), nil
	})
	c.registry.Register(ScreenIDSettings, func(ctx ScreenContext) (*Screen, error) {
		return BuildSettingsScreen(ctx.Username), nil
	})
	c.registry.Register(ScreenIDHelp, func(ctx ScreenContext) (*Screen, error) {
		return BuildHelpScreenWithCommands(ctx.Username, ctx.Commands), nil
	})
	c.registry.Register(ScreenIDStatus, func(ctx ScreenContext) (*Screen, error) {
		return BuildStatusScreen(ctx.Username, ctx.Uptime, ctx.Engine), nil
	})
}

func (c *Controller) buildRegistered(id ScreenID, data ScreenContext) (*Screen, error) {
	if c.registry == nil {
		return nil, fmt.Errorf("assistant/menu: screen registry is unavailable")
	}
	return c.registry.Build(id, data)
}

func BuildStartScreen(botUsername string, uptime time.Duration) *Screen {
	return BuildStartScreenWithCommands(botUsername, uptime, nil)
}

func BuildStartScreenWithCommands(botUsername string, uptime time.Duration, cmds []core.Command) *Screen {
	if botUsername == "" {
		botUsername = "GoUltroidBot"
	}
	card := ui.NewCard("GoUltroid Assistant").
		WithIcon("🤖").
		WithHeader("Control center for your userbot and assistant.").
		AddField("Bot", "@"+botUsername).
		AddField("Status", "🟢 Online & ready").
		AddField("Uptime", appStatus.FormatDuration(uptime))
	if len(cmds) > 0 {
		card.AddField("Assistant commands", fmt.Sprintf("%d available", len(cmds)))
	}
	card.WithFooter("<i>Choose an area below. Menus are generated from the registered capabilities.</i>")
	screen := NewScreen(ScreenIDStart, "", card.Render())
	screen.AddRow(NewButton("⚙️ Settings", "a1:assistant:settings"), NewButton("📚 Help", "a1:assistant:help"))
	screen.AddRow(NewButton("📊 Status", "a1:assistant:status"), NewButton("🏓 Ping", "a1:assistant:ping"))
	screen.AddRow(NewButton("❌ Close", "a1:assistant:close"))
	return screen
}

func BuildSettingsScreen(botUsername string) *Screen {
	screen := NewScreen(ScreenIDSettings, "⚙️ Settings", "")
	screen.Body = ui.NewCard("Assistant Settings").
		WithIcon("⚙️").
		WithHeader("Manage persistent userbot configuration through the central settings service.").
		WithFooter("<i>Changes are validated, persisted, and applied by the shared settings subsystem.</i>").
		Render()
	screen.AddRow(NewButton("📂 Open Settings Dashboard", "a1:settings:home"))
	screen.AddRow(NewButton("🏠 Back to Menu", "a1:assistant:start"), NewButton("❌ Close", "a1:assistant:close"))
	return screen
}

func BuildHelpScreen(botUsername string) *Screen {
	return BuildHelpScreenWithCommands(botUsername, nil)
}

func BuildHelpScreenWithCommands(botUsername string, cmds []core.Command) *Screen {
	screen := NewScreen(ScreenIDHelp, "📚 Help", "")
	if len(cmds) == 0 {
		screen.Body = ui.NewCard("Command Browser").
			WithIcon("📚").
			WithHeader("Browse the commands exposed by the Assistant surface.").
			WithRaw("<code>/start</code> — open this dashboard\n<code>/help</code> — command documentation\n<code>/status</code> — runtime status\n<code>/ping</code> — connectivity check").
			WithFooter("<i>Use /help &lt;module&gt; or /help &lt;command&gt; for details.</i>").
			Render()
	} else {
		categories := make(map[string]int)
		for _, cmd := range cmds {
			category := cmd.Category
			if category == "" {
				category = "General"
			}
			categories[category]++
		}

		categoryNames := make([]string, 0, len(categories))
		for category := range categories {
			categoryNames = append(categoryNames, category)
		}
		sort.Strings(categoryNames)

		body := fmt.Sprintf("<i>%d commands across %d modules.</i>\n\n", len(cmds), len(categoryNames))
		for _, category := range categoryNames {
			body += fmt.Sprintf("📂 <b>%s</b> <code>(%d)</code>\n", ui.EscapeHTML(category), categories[category])
		}
		body += "\n💡 <i>Use <code>/help &lt;module&gt;</code> or <code>/help &lt;command&gt;</code> for details.</i>"

		screen.Body = ui.NewCard("Assistant Commands").
			WithIcon("📚").
			WithHeader("Command browser").
			WithRaw(body).
			WithFooter("<i>The command registry is the single source of truth.</i>").
			Render()
	}
	screen.AddRow(NewButton("⚙️ Settings", "a1:assistant:settings"), NewButton("📊 Status", "a1:assistant:status"))
	screen.AddRow(NewButton("🏠 Back to Menu", "a1:assistant:start"), NewButton("❌ Close", "a1:assistant:close"))
	return screen
}

func BuildStatusScreen(botUsername string, uptime time.Duration, engine string) *Screen {
	if botUsername == "" {
		botUsername = "GoUltroidBot"
	}
	if engine == "" {
		engine = "GoUltroid (MTProto)"
	}
	body := ui.NewCard("System Status").
		WithIcon("📊").
		WithHeader("Assistant runtime health and transport information.").
		AddField("Assistant", "@"+botUsername).
		AddField("Status", "🟢 Operational").
		AddField("Uptime", appStatus.FormatDuration(uptime)).
		AddField("Engine", engine).
		AddField("Callbacks", "🟢 Active").
		WithFooter("<i>Refresh to read the latest runtime state.</i>").
		Render()
	screen := NewScreen(ScreenIDStatus, "", body)
	screen.AddRow(NewButton("🔄 Refresh", "a1:assistant:status"), NewButton("🏠 Home", "a1:assistant:start"), NewButton("❌ Close", "a1:assistant:close"))
	return screen
}

func (c *Controller) lockInstance(tx *callback.Transaction) func() {
	if c.instances != nil && tx != nil {
		return c.instances.LockInstance(tx.Target.ChatID(), tx.Target.MessageID())
	}
	return func() {}
}

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

func (c *Controller) RegisterInstance(inst MenuInstance) {
	if c.instances != nil {
		c.instances.Register(inst)
	}
}

func (c *Controller) renderRegistered(id ScreenID, data ScreenContext) (string, tg.ReplyMarkupClass, error) {
	screen, err := c.buildRegistered(id, data)
	if err != nil {
		return "", nil, err
	}
	if c.renderer == nil {
		return screen.Text(), nil, nil
	}
	text, markup := c.renderer(screen)
	return text, markup, nil
}

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
	commands := func() []core.Command {
		if c.cmdSource == nil {
			return nil
		}
		return c.cmdSource.CommandsForSurface(execution.SourceAssistant)
	}
	contextData := func() ScreenContext {
		return ScreenContext{Username: username(), Uptime: uptime(), Engine: "GoUltroid (MTProto)", Commands: commands()}
	}

	edit := func(ctx context.Context, tx *callback.Transaction, id ScreenID) error {
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		unlock := c.lockInstance(tx)
		defer unlock()
		text, markup, err := c.renderRegistered(id, contextData())
		if err != nil {
			return err
		}
		if c.instances != nil {
			c.instances.UpdateScreen(tx.Target.ChatID(), tx.Target.MessageID(), id)
		}
		return tx.Edit(ctx, text, markup)
	}

	r.Register("assistant", "start", func(ctx context.Context, tx *callback.Transaction) error { return edit(ctx, tx, ScreenIDStart) })
	r.Register("assistant", "settings", func(ctx context.Context, tx *callback.Transaction) error { return edit(ctx, tx, ScreenIDSettings) })
	r.Register("assistant", "help", func(ctx context.Context, tx *callback.Transaction) error { return edit(ctx, tx, ScreenIDHelp) })
	r.Register("assistant", "status", func(ctx context.Context, tx *callback.Transaction) error { return edit(ctx, tx, ScreenIDStatus) })
	r.Register("assistant", "ping", func(ctx context.Context, tx *callback.Transaction) error {
		unlock := c.lockInstance(tx)
		defer unlock()
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		return tx.Answer(ctx, "🏓 Pong!", true)
	})
	r.Register("assistant", "close", func(ctx context.Context, tx *callback.Transaction) error {
		unlock := c.lockInstance(tx)
		defer unlock()
		if _, err := c.validateSession(ctx, tx); err != nil {
			return err
		}
		_ = tx.Answer(ctx, "Menu closed", false)
		c.instances.Invalidate(tx.Target.ChatID(), tx.Target.MessageID())
		return tx.Delete(ctx)
	})
}
