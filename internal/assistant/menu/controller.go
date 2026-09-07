package menu

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
type CommandSource interface{ CommandsForSurface(source execution.Source) []core.Command }

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
		renderer: renderer, instances: NewMemoryInstanceStore(DefaultMenuTTL),
		registry: NewRegistry(), pending: make(map[int64]pendingSettingInput),
	}
	c.registerDefaultScreens()
	return c
}

func (c *Controller) SetCommandSource(cs CommandSource) { c.cmdSource = cs }
func (c *Controller) Instances() InstanceStore { return c.instances }
func (c *Controller) Registry() *Registry { return c.registry }

type ScreenContext struct {
	Username string
	Uptime   time.Duration
	Engine   string
	Commands []core.Command
}

func (c *Controller) registerDefaultScreens() {
	c.registry.Register(ScreenIDStart, func(ctx any) (*Screen, error) { data := screenContext(ctx); return BuildStartScreenWithCommands(data.Username, data.Uptime, data.Commands), nil })
	c.registry.Register(ScreenIDSettings, func(ctx any) (*Screen, error) { data := screenContext(ctx); return BuildSettingsScreen(data.Username), nil })
	c.registry.Register(ScreenIDHelp, func(ctx any) (*Screen, error) { data := screenContext(ctx); return BuildHelpScreenWithCommands(data.Username, data.Commands), nil })
	c.registry.Register(ScreenIDStatus, func(ctx any) (*Screen, error) { data := screenContext(ctx); return BuildStatusScreen(data.Username, data.Uptime, data.Engine), nil })
}

func screenContext(ctx any) ScreenContext {
	if value, ok := ctx.(ScreenContext); ok { return value }
	return ScreenContext{Username: "GoUltroidBot", Engine: "GoUltroid (MTProto)"}
}

func (c *Controller) buildRegistered(id ScreenID, data ScreenContext) (*Screen, error) {
	if c.registry == nil { return nil, fmt.Errorf("assistant/menu: screen registry is unavailable") }
	return c.registry.Build(id, data)
}

func BuildStartScreen(botUsername string, uptime time.Duration) *Screen { return BuildStartScreenWithCommands(botUsername, uptime, nil) }

func BuildStartScreenWithCommands(botUsername string, uptime time.Duration, cmds []core.Command) *Screen {
	if botUsername == "" { botUsername = "GoUltroidBot" }
	card := ui.NewCard("GoUltroid Assistant").WithIcon("🤖").WithHeader("Control center for your userbot and assistant.").AddField("Bot", "@"+botUsername).AddField("Status", "🟢 Online & ready").AddField("Uptime", appStatus.FormatDuration(uptime))
	if len(cmds) > 0 { card.AddField("Assistant commands", fmt.Sprintf("%d available", len(cmds))) }
	card.WithFooter("<i>Choose an area below. Menus are generated from the registered capabilities.</i>")
	screen := NewScreen(ScreenIDStart, "", card.Render())
	screen.AddRow(NewButton("⚙️ Settings", "a1:assistant:settings"), NewButton("📚 Help", "a1:assistant:help"))
	screen.AddRow(NewButton("📊 Status", "a1:assistant:status"), NewButton("🏓 Ping", "a1:assistant:ping"))
	screen.AddRow(NewButton("❌ Close", "a1:assistant:close"))
	return screen
}

func BuildSettingsScreen(botUsername string) *Screen {
	screen := NewScreen(ScreenIDSettings, "⚙️ Settings", "")
	screen.Body = ui.NewCard("Assistant Settings").WithIcon("⚙️").WithHeader("Manage persistent userbot configuration through the central settings service.").WithFooter("<i>Changes are validated, persisted, and applied by the shared settings subsystem.</i>").Render()
	screen.AddRow(NewButton("📂 Open Settings Dashboard", "a1:settings:home"))
	screen.AddRow(NewButton("🏠 Back to Menu", "a1:assistant:start"), NewButton("❌ Close", "a1:assistant:close"))
	return screen
}

func BuildHelpScreen(botUsername string) *Screen { return BuildHelpScreenWithCommands(botUsername, nil) }

func BuildHelpScreenWithCommands(botUsername string, cmds []core.Command) *Screen {
	screen := NewScreen(ScreenIDHelp, "📚 Help", "")
	if len(cmds) == 0 {
		screen.Body = ui.NewCard("Command Browser").WithIcon("📚").WithHeader("Browse the commands exposed by the Assistant surface.").WithRaw("<code>/start</code> — open this dashboard\n<code>/help</code> — command documentation\n<code>/status</code> — runtime status\n<code>/ping</code> — connectivity check").Render()
	} else {
		sorted := append([]core.Command(nil), cmds...)
		sort.Slice(sorted, func(i, j int) bool {
			ci, cj := sorted[i].Category, sorted[j].Category
			if ci == "" { ci = "General" }; if cj == "" { cj = "General" }
			if ci != cj { return ci < cj }; return sorted[i].Name < sorted[j].Name
		})
		var b strings.Builder
		category := ""
		for _, cmd := range sorted {
			cat := cmd.Category; if cat == "" { cat = "General" }
			if cat != category { if category != "" { b.WriteString("\n") }; b.WriteString(fmt.Sprintf("📂 <b>%s</b>\n", ui.EscapeHTML(cat))); category = cat }
			desc := cmd.Description; if desc == "" { desc = "No description" }
			b.WriteString(fmt.Sprintf("• <code>/%s</code> — %s\n", ui.EscapeHTML(cmd.Name), ui.EscapeHTML(desc)))
		}
		screen.Body = ui.NewCard("Assistant Commands").WithIcon("📚").WithHeader(fmt.Sprintf("%d commands are available on the Assistant surface.", len(sorted))).WithRaw(strings.TrimSpace(b.String())).WithFooter("<i>Use /help &lt;command&gt; for detailed documentation.</i>").Render()
	}
	screen.AddRow(NewButton("⚙️ Settings", "a1:assistant:settings"), NewButton("📊 Status", "a1:assistant:status"))
	screen.AddRow(NewButton("🏠 Back to Menu", "a1:assistant:start"), NewButton("❌ Close", "a1:assistant:close"))
	return screen
}

func BuildStatusScreen(botUsername string, uptime time.Duration, engine string) *Screen {
	if botUsername == "" { botUsername = "GoUltroidBot" }; if engine == "" { engine = "GoUltroid (MTProto)" }
	body := ui.NewCard("System Status").WithIcon("📊").WithHeader("Assistant runtime health and transport information.").AddField("Assistant", "@"+botUsername).AddField("Status", "🟢 Operational").AddField("Uptime", appStatus.FormatDuration(uptime)).AddField("Engine", engine).AddField("Callbacks", "🟢 Active").WithFooter("<i>Refresh to read the latest runtime state.</i>").Render()
	screen := NewScreen(ScreenIDStatus, "", body)
	screen.AddRow(NewButton("🔄 Refresh", "a1:assistant:status"), NewButton("🏠 Home", "a1:assistant:start"), NewButton("❌ Close", "a1:assistant:close"))
	return screen
}

func (c *Controller) lockInstance(tx *callback.Transaction) func() { if c.instances != nil && tx != nil { return c.instances.LockInstance(tx.Target.ChatID(), tx.Target.MessageID()) }; return func() {} }

func (c *Controller) validateSession(ctx context.Context, tx *callback.Transaction) (*MenuInstance, error) {
	if c.instances == nil { return nil, callback.ErrSessionExpired }
	if tx == nil || !tx.Target.IsValid() || tx.UserID == 0 { if tx != nil { _ = tx.Answer(ctx, "Session expired, send /start again", true) }; return nil, callback.ErrSessionExpired }
	inst, ok := c.instances.Get(tx.Target.ChatID(), tx.Target.MessageID())
	if !ok || inst == nil { _ = tx.Answer(ctx, "Session expired, send /start again", true); return nil, callback.ErrSessionExpired }
	if inst.OwnerID == 0 || tx.UserID != inst.OwnerID { _ = tx.Answer(ctx, "⚠️ You do not own this menu!", true); return nil, callback.ErrUnauthorized }
	return inst, nil
}

func (c *Controller) RegisterInstance(inst MenuInstance) { if c.instances != nil { c.instances.Register(inst) } }

func (c *Controller) renderRegistered(id ScreenID, data ScreenContext) (string, tg.ReplyMarkupClass, error) {
	screen, err := c.buildRegistered(id, data); if err != nil { return "", nil, err }
	if c.renderer == nil { return screen.Text(), nil, nil }
	text, markup := c.renderer(screen); return text, markup, nil
}

func (c *Controller) AttachRoutes(r *callback.Router, getUsername func() string, getStartTime func() time.Time) {
	if r == nil { return }
	uptime := func() time.Duration { if getStartTime != nil { return time.Since(getStartTime()) }; return 0 }
	username := func() string { if getUsername != nil { return getUsername() }; return "GoUltroidBot" }
	commands := func() []core.Command { if c.cmdSource == nil { return nil }; return c.cmdSource.CommandsForSurface(execution.SourceAssistant) }
	contextData := func() ScreenContext { return ScreenContext{Username: username(), Uptime: uptime(), Engine: "GoUltroid (MTProto)", Commands: commands()} }

	edit := func(ctx context.Context, tx *callback.Transaction, id ScreenID) error {
		if _, err := c.validateSession(ctx, tx); err != nil { return err }
		unlock := c.lockInstance(tx); defer unlock()
		text, markup, err := c.renderRegistered(id, contextData()); if err != nil { return err }
		if c.instances != nil { c.instances.UpdateScreen(tx.Target.ChatID(), tx.Target.MessageID(), id) }
		return tx.Edit(ctx, text, markup)
	}

	r.Register("assistant", "start", func(ctx context.Context, tx *callback.Transaction) error { return edit(ctx, tx, ScreenIDStart) })
	r.Register("assistant", "settings", func(ctx context.Context, tx *callback.Transaction) error { return edit(ctx, tx, ScreenIDSettings) })
	r.Register("assistant", "help", func(ctx context.Context, tx *callback.Transaction) error { return edit(ctx, tx, ScreenIDHelp) })
	r.Register("assistant", "status", func(ctx context.Context, tx *callback.Transaction) error { return edit(ctx, tx, ScreenIDStatus) })
	r.Register("assistant", "ping", func(ctx context.Context, tx *callback.Transaction) error { unlock := c.lockInstance(tx); defer unlock(); if _, err := c.validateSession(ctx, tx); err != nil { return err }; return tx.Answer(ctx, "🏓 Pong!", true) })
	r.Register("assistant", "close", func(ctx context.Context, tx *callback.Transaction) error { unlock := c.lockInstance(tx); defer unlock(); if _, err := c.validateSession(ctx, tx); err != nil { return err }; _ = tx.Answer(ctx, "Menu closed", false); c.instances.Invalidate(tx.Target.ChatID(), tx.Target.MessageID()); return tx.Delete(ctx) })
}
