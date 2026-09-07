package menu

import (
	"context"
	"fmt"
	"sort"
	"strconv"
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
type CommandSource interface { CommandsForSurface(source execution.Source) []core.Command }

type Controller struct {
	renderer RendererFunc
	instances InstanceStore
	cmdSource CommandSource
	registry *Registry
	pendingMu sync.Mutex
	pending map[int64]pendingSettingInput
}

func NewController(renderer RendererFunc) *Controller {
	c := &Controller{renderer: renderer, instances: NewMemoryInstanceStore(DefaultMenuTTL), registry: NewRegistry(), pending: make(map[int64]pendingSettingInput)}
	c.registerDefaultScreens()
	return c
}
func (c *Controller) SetCommandSource(cs CommandSource) { c.cmdSource = cs }
func (c *Controller) Instances() InstanceStore { return c.instances }
func (c *Controller) Registry() *Registry { return c.registry }

type ScreenContext struct { Username string; Uptime time.Duration; Engine string; Commands []core.Command }

func (c *Controller) registerDefaultScreens() {
	c.registry.Register(ScreenIDStart, func(ctx ScreenContext) (*Screen, error) { return BuildStartScreenWithCommands(ctx.Username, ctx.Uptime, ctx.Commands), nil })
	c.registry.Register(ScreenIDSettings, func(ctx ScreenContext) (*Screen, error) { return BuildSettingsScreen(ctx.Username), nil })
	c.registry.Register(ScreenIDHelp, func(ctx ScreenContext) (*Screen, error) { return BuildHelpScreenWithCommands(ctx.Username, ctx.Commands), nil })
	c.registry.Register(ScreenIDStatus, func(ctx ScreenContext) (*Screen, error) { return BuildStatusScreen(ctx.Username, ctx.Uptime, ctx.Engine), nil })
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
	screen.Body = ui.NewCard("Assistant Settings").WithIcon("⚙️").WithHeader("Configure persistent userbot behavior. Select a category to browse its settings.").WithFooter("<i>All changes use the central settings service and are validated before persistence.</i>").Render()
	screen.AddRow(NewButton("📂 Open Settings Dashboard", "a1:settings:home"))
	screen.AddRow(NewButton("🏠 Back to Menu", "a1:assistant:start"), NewButton("❌ Close", "a1:assistant:close"))
	return screen
}
func BuildHelpScreen(botUsername string) *Screen { return BuildHelpScreenWithCommands(botUsername, nil) }
func BuildHelpScreenWithCommands(botUsername string, cmds []core.Command) *Screen { return buildHelpPage(botUsername, sortedCommands(cmds), 0) }

const ( helpModulesPerPage = 6; helpCommandsPerPage = 6 )
func sortedCommands(cmds []core.Command) []core.Command {
	out := append([]core.Command(nil), cmds...)
	sort.SliceStable(out, func(i, j int) bool { ci, cj := out[i].Category, out[j].Category; if ci == "" { ci = "General" }; if cj == "" { cj = "General" }; if ci != cj { return strings.ToLower(ci) < strings.ToLower(cj) }; return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}
func commandCategories(cmds []core.Command) []string {
	seen := make(map[string]struct{}); for _, cmd := range cmds { category := cmd.Category; if category == "" { category = "General" }; seen[category] = struct{}{} }
	categories := make([]string, 0, len(seen)); for category := range seen { categories = append(categories, category) }; sort.Strings(categories); return categories
}
func commandsInCategory(cmds []core.Command, category string) []core.Command {
	out := make([]core.Command, 0); for _, cmd := range cmds { cat := cmd.Category; if cat == "" { cat = "General" }; if cat == category { out = append(out, cmd) } }; return out
}
func buildHelpPage(botUsername string, cmds []core.Command, page int) *Screen {
	categories := commandCategories(cmds); pages := maxPage(len(categories), helpModulesPerPage); page = clampPage(page, pages); start := page * helpModulesPerPage; end := min(start+helpModulesPerPage, len(categories))
	card := ui.NewCard("Command Browser").WithIcon("📚").WithHeader("Browse Assistant commands by module.")
	if len(cmds) == 0 { card.WithRaw("<i>No Assistant commands are currently registered.</i>") } else { card.AddField("Commands", strconv.Itoa(len(cmds))).AddField("Modules", strconv.Itoa(len(categories))).WithFooter("<i>Tap a module to browse its commands. Tap a command to see its full help.</i>") }
	screen := NewScreen(ScreenIDHelp, "📚 Help", card.Render())
	for i := start; i < end; i++ { category := categories[i]; screen.AddRow(NewButton(fmt.Sprintf("📂 %s · %d", category, len(commandsInCategory(cmds, category))), "a1:assistant:help_module:"+strconv.Itoa(i))) }
	if pages > 1 { screen.AddRow(helpPagerButtons("assistant:help_page", page, pages)) }
	screen.AddRow(NewButton("🏠 Home", "a1:assistant:start"), NewButton("❌ Close", "a1:assistant:close")); return screen
}
func buildHelpModulePage(cmds []core.Command, moduleIndex, page int) *Screen {
	categories := commandCategories(cmds); if moduleIndex < 0 || moduleIndex >= len(categories) { return buildHelpPage("", cmds, 0) }
	category := categories[moduleIndex]; moduleCommands := commandsInCategory(cmds, category); pages := maxPage(len(moduleCommands), helpCommandsPerPage); page = clampPage(page, pages); start := page * helpCommandsPerPage; end := min(start+helpCommandsPerPage, len(moduleCommands))
	card := ui.NewCard(category).WithIcon("📂").WithHeader("Select a command to view its documentation.").AddField("Commands", strconv.Itoa(len(moduleCommands))).WithFooter("<i>Tap a command to open detailed help.</i>")
	screen := NewScreen(ScreenIDHelp, "📚 Help", card.Render())
	for i := start; i < end; i++ { cmd := moduleCommands[i]; label := "/"+cmd.Name; if cmd.Description != "" { label += " — "+truncateRunes(cmd.Description, 42) }; screen.AddRow(NewButton(label, "a1:assistant:help_command:"+fmt.Sprintf("%d:%d", moduleIndex, i))) }
	if pages > 1 { screen.AddRow(helpPagerButtons(fmt.Sprintf("assistant:help_module_page:%d", moduleIndex), page, pages)) }
	screen.AddRow(NewButton("« Modules", "a1:assistant:help"), NewButton("🏠 Home", "a1:assistant:start")); return screen
}
func buildHelpCommandScreen(cmd core.Command, moduleIndex int) *Screen {
	name := "/"+cmd.Name; card := ui.NewCard(name).WithIcon("📖").WithHeader(cmd.Description)
	if cmd.Usage != "" { card.AddField("Usage", "<code>"+ui.EscapeHTML(cmd.Usage)+"</code>") }
	if len(cmd.Aliases) > 0 { aliases := make([]string, 0, len(cmd.Aliases)); for _, alias := range cmd.Aliases { aliases = append(aliases, "/"+alias) }; card.AddField("Aliases", ui.EscapeHTML(strings.Join(aliases, ", "))) }
	category := cmd.Category; if category == "" { category = "General" }; card.AddField("Module", ui.EscapeHTML(category)).AddField("Permission", cmd.Permission.String())
	if cmd.GroupOnly { card.AddField("Context", "Group only") }; if cmd.PrivateOnly { card.AddField("Context", "Private only") }; if cmd.ReplyOnly { card.AddField("Context", "Reply required") }; if cmd.Cooldown > 0 { card.AddField("Cooldown", cmd.Cooldown.String()) }; if cmd.Timeout > 0 { card.AddField("Timeout", cmd.Timeout.String()) }
	card.WithFooter("<i>Execute it with /"+ui.EscapeHTML(cmd.Name)+".</i>"); screen := NewScreen(ScreenIDHelp, "📚 Help", card.Render()); screen.AddRow(NewButton("« Commands", fmt.Sprintf("a1:assistant:help_module:%d", moduleIndex))); screen.AddRow(NewButton("📚 Modules", "a1:assistant:help"), NewButton("🏠 Home", "a1:assistant:start")); return screen
}
func helpPagerButtons(action string, page, pages int) ui.ButtonRow {
	row := ui.ButtonRow{}
	if page > 0 { row = append(row, NewButton("◀️ Previous", "a1:"+action+":"+strconv.Itoa(page-1))) } else { row = append(row, NewButton("·", "a1:assistant:noop")) }
	row = append(row, NewButton(fmt.Sprintf("%d / %d", page+1, pages), "a1:assistant:noop"))
	if page+1 < pages { row = append(row, NewButton("Next ▶️", "a1:"+action+":"+strconv.Itoa(page+1))) } else { row = append(row, NewButton("·", "a1:assistant:noop")) }; return row
}
func maxPage(total, pageSize int) int { if total <= 0 { return 1 }; return (total+pageSize-1)/pageSize }
func clampPage(page, pages int) int { if page < 0 { return 0 }; if page >= pages { return pages-1 }; return page }
func min(a,b int) int { if a < b { return a }; return b }
func truncateRunes(s string, max int) string { r := []rune(strings.TrimSpace(s)); if len(r) <= max { return string(r) }; if max <= 1 { return string(r[:max]) }; return string(r[:max-1])+"…" }

func BuildStatusScreen(botUsername string, uptime time.Duration, engine string) *Screen {
	if botUsername == "" { botUsername = "GoUltroidBot" }; if engine == "" { engine = "GoUltroid (MTProto)" }
	body := ui.NewCard("System Status").WithIcon("📊").WithHeader("Assistant runtime health and transport information.").AddField("Assistant", "@"+botUsername).AddField("Status", "🟢 Operational").AddField("Uptime", appStatus.FormatDuration(uptime)).AddField("Engine", engine).AddField("Callbacks", "🟢 Active").WithFooter("<i>Refresh to read the latest runtime state.</i>").Render()
	screen := NewScreen(ScreenIDStatus, "", body); screen.AddRow(NewButton("🔄 Refresh", "a1:assistant:status"), NewButton("🏠 Home", "a1:assistant:start"), NewButton("❌ Close", "a1:assistant:close")); return screen
}
func (c *Controller) lockInstance(tx *callback.Transaction) func() { if c.instances != nil && tx != nil { return c.instances.LockInstance(tx.Target.ChatID(), tx.Target.MessageID()) }; return func() {} }
func (c *Controller) validateSession(ctx context.Context, tx *callback.Transaction) (*MenuInstance, error) {
	if c.instances == nil { return nil, callback.ErrSessionExpired }; if tx == nil || !tx.Target.IsValid() || tx.UserID == 0 { if tx != nil { _ = tx.Answer(ctx, "Session expired, send /start again", true) }; return nil, callback.ErrSessionExpired }
	inst, ok := c.instances.Get(tx.Target.ChatID(), tx.Target.MessageID()); if !ok || inst == nil { _ = tx.Answer(ctx, "Session expired, send /start again", true); return nil, callback.ErrSessionExpired }; if inst.OwnerID == 0 || tx.UserID != inst.OwnerID { _ = tx.Answer(ctx, "⚠️ You do not own this menu!", true); return nil, callback.ErrUnauthorized }; return inst,nil
}
func (c *Controller) RegisterInstance(inst MenuInstance) { if c.instances != nil { c.instances.Register(inst) } }
func (c *Controller) renderRegistered(id ScreenID, data ScreenContext) (string, tg.ReplyMarkupClass, error) { screen,err := c.buildRegistered(id,data); if err != nil { return "",nil,err }; if c.renderer == nil { return screen.Text(),nil,nil }; text,markup := c.renderer(screen); return text,markup,nil }
func (c *Controller) AttachRoutes(r *callback.Router, getUsername func() string, getStartTime func() time.Time) {
	if r == nil { return }; uptime := func() time.Duration { if getStartTime != nil { return time.Since(getStartTime()) }; return 0 }; username := func() string { if getUsername != nil { return getUsername() }; return "GoUltroidBot" }; commands := func() []core.Command { if c.cmdSource == nil { return nil }; return sortedCommands(c.cmdSource.CommandsForSurface(execution.SourceAssistant)) }; contextData := func() ScreenContext { return ScreenContext{Username:username(),Uptime:uptime(),Engine:"GoUltroid (MTProto)",Commands:commands()} }
	edit := func(ctx context.Context, tx *callback.Transaction, id ScreenID) error { if _,err:=c.validateSession(ctx,tx); err!=nil{return err}; unlock:=c.lockInstance(tx); defer unlock(); text,markup,err:=c.renderRegistered(id,contextData()); if err!=nil{return err}; if c.instances!=nil{c.instances.UpdateScreen(tx.Target.ChatID(),tx.Target.MessageID(),id)}; return tx.Edit(ctx,text,markup) }
	r.Register("assistant","start",func(ctx context.Context,tx *callback.Transaction)error{return edit(ctx,tx,ScreenIDStart)}); r.Register("assistant","settings",func(ctx context.Context,tx *callback.Transaction)error{return edit(ctx,tx,ScreenIDSettings)}); r.Register("assistant","help",func(ctx context.Context,tx *callback.Transaction)error{return edit(ctx,tx,ScreenIDHelp)})
	r.Register("assistant","help_page",func(ctx context.Context,tx *callback.Transaction)error{if _,err:=c.validateSession(ctx,tx);err!=nil{return err}; page,err:=strconv.Atoi(tx.Payload.State);if err!=nil{return fmt.Errorf("invalid help page: %w",err)};unlock:=c.lockInstance(tx);defer unlock();text,markup:=c.renderer(buildHelpPage(username(),commands(),page));return tx.Edit(ctx,text,markup)})
	r.Register("assistant","help_module",func(ctx context.Context,tx *callback.Transaction)error{if _,err:=c.validateSession(ctx,tx);err!=nil{return err};moduleIndex,err:=strconv.Atoi(tx.Payload.State);if err!=nil{return fmt.Errorf("invalid help module: %w",err)};unlock:=c.lockInstance(tx);defer unlock();text,markup:=c.renderer(buildHelpModulePage(commands(),moduleIndex,0));return tx.Edit(ctx,text,markup)})
	r.Register("assistant","help_module_page",func(ctx context.Context,tx *callback.Transaction)error{if _,err:=c.validateSession(ctx,tx);err!=nil{return err};parts:=strings.Split(tx.Payload.State,":");if len(parts)!=2{return fmt.Errorf("invalid help module pagination state")};moduleIndex,err:=strconv.Atoi(parts[0]);if err!=nil{return err};page,err:=strconv.Atoi(parts[1]);if err!=nil{return err};unlock:=c.lockInstance(tx);defer unlock();text,markup:=c.renderer(buildHelpModulePage(commands(),moduleIndex,page));return tx.Edit(ctx,text,markup)})
	r.Register("assistant","help_command",func(ctx context.Context,tx *callback.Transaction)error{if _,err:=c.validateSession(ctx,tx);err!=nil{return err};parts:=strings.Split(tx.Payload.State,":");if len(parts)!=2{return fmt.Errorf("invalid help command state")};moduleIndex,err:=strconv.Atoi(parts[0]);if err!=nil{return err};commandIndex,err:=strconv.Atoi(parts[1]);if err!=nil{return err};categories:=commandCategories(commands());if moduleIndex<0||moduleIndex>=len(categories){return fmt.Errorf("help module out of range")};moduleCommands:=commandsInCategory(commands(),categories[moduleIndex]);if commandIndex<0||commandIndex>=len(moduleCommands){return fmt.Errorf("help command out of range")};unlock:=c.lockInstance(tx);defer unlock();text,markup:=c.renderer(buildHelpCommandScreen(moduleCommands[commandIndex],moduleIndex));return tx.Edit(ctx,text,markup)})
	r.Register("assistant","noop",func(ctx context.Context,tx *callback.Transaction)error{return tx.Answer(ctx,"",false)}); r.Register("assistant","status",func(ctx context.Context,tx *callback.Transaction)error{return edit(ctx,tx,ScreenIDStatus)})
	r.Register("assistant","ping",func(ctx context.Context,tx *callback.Transaction)error{unlock:=c.lockInstance(tx);defer unlock();if _,err:=c.validateSession(ctx,tx);err!=nil{return err};return tx.Answer(ctx,"🏓 Pong!",true)})
	r.Register("assistant","close",func(ctx context.Context,tx *callback.Transaction)error{unlock:=c.lockInstance(tx);defer unlock();if _,err:=c.validateSession(ctx,tx);err!=nil{return err};_=tx.Answer(ctx,"Menu closed",false);c.instances.Invalidate(tx.Target.ChatID(),tx.Target.MessageID());return tx.Delete(ctx)})
}
