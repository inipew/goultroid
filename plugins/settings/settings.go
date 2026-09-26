package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/ui"
	"github.com/inipew/goultroid/plugins/settings/usecase"
	"go.uber.org/zap"
)

var _ plugin.Plugin = (*Plugin)(nil)

// SettingTarget specifies the exact namespace and key being inspected or modified.
type SettingTarget struct {
	Namespace string `json:"ns"`
	Key       string `json:"k"`
}

// MenuState is bounded server-side interaction state shared by native and Assistant a2 views.
type MenuState struct {
	Scope       settings.SettingScope `json:"sc"`
	ScopeID     int64                 `json:"sid"`
	Category    string                `json:"cat"`
	Page        int                   `json:"p"`
	Selected    string                `json:"sel,omitempty"` // ns:key (kept for JSON/backward compatibility)
	Target      *SettingTarget        `json:"tgt,omitempty"`
	OwnerID     int64                 `json:"ow"`
	ChatID      int64                 `json:"cid,omitempty"`
	ActionValue string                `json:"act_val,omitempty"`
}

// ScopeRef returns the typed scope reference for this menu state.
func (s MenuState) ScopeRef() settings.ScopeRef {
	return settings.ScopeRef{Type: s.Scope, ID: s.ScopeID}
}

// ValidateScope checks whether the current scope is consistent (e.g. chat scope must have non-zero ID).
func (s MenuState) ValidateScope() error {
	return s.ScopeRef().Validate()
}

// GetTarget extracts the namespace and key safely from Target or Selected.
func (s *MenuState) GetTarget() (ns, key string) {
	if s.Target != nil && s.Target.Namespace != "" && s.Target.Key != "" {
		return s.Target.Namespace, s.Target.Key
	}
	if s.Selected != "" {
		parts := strings.Split(s.Selected, ":")
		if len(parts) >= 2 {
			return parts[0], parts[1]
		}
	}
	return "", ""
}

// SetTarget sets the target namespace and key, syncing Target and Selected.
func (s *MenuState) SetTarget(ns, key string) {
	s.Target = &SettingTarget{Namespace: ns, Key: key}
	s.Selected = ns + ":" + key
}

// Plugin provides interactive settings management via dashboard and CLI.
type Plugin struct {
	service   *settings.Service
	logger    *zap.Logger
	setUC     *usecase.SetSettingUseCase
	resetUC   *usecase.ResetSettingUseCase
	native    nativeRuntimeState
	assistant assistantRuntimeState
}

// New creates a new settings plugin instance.
func New(service *settings.Service) *Plugin {
	return &Plugin{
		service:  service,
		logger:   zap.NewNop(),
		setUC:    &usecase.SetSettingUseCase{Service: service},
		resetUC:  &usecase.ResetSettingUseCase{Service: service},
	}
}

// SetLogger sets the structured logger.
func (p *Plugin) SetLogger(l *zap.Logger) {
	if l != nil {
		p.logger = l
	}
}

func (p *Plugin) Name() string {
	return "settings"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Description() string {
	return "Hierarchical settings subsystem and interactive Telegram configuration dashboard"
}

// Capabilities declares the capabilities provided by this plugin (§4 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "settings",
			Name:        "Settings",
			Description: "Hierarchical settings management and dashboard",
			Category:    "Settings",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "settings",
			Description: "Open interactive settings dashboard",
			Usage:       ".settings [category]",
			Category:    "Settings",
			Permission:  core.PermissionOwner,
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			Handler:     p.handleSettingsCommand,
		},
		{
			Name:        "config",
			Description: "CLI command to inspect, set, or reset configuration",
			Usage:       ".config <get|set|reset|list|history|export> [args...]",
			Category:    "Settings",
			Permission:  core.PermissionOwner,
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			Handler:     p.handleConfigCommand,
		},
	}
}

// =================== Interactive Dashboard Handler ===================

func (p *Plugin) handleSettingsCommand(ctx *core.Context) error {
	cat := ""
	if len(ctx.Args) > 0 {
		cat = strings.ToLower(ctx.Args[0])
	}
	page := 1
	if len(ctx.Args) > 1 {
		if parsed, err := strconv.Atoi(ctx.Args[1]); err == nil && parsed > 0 {
			page = parsed
		}
	}

	state := MenuState{
		Scope:    settings.ScopeGlobal,
		ScopeID:  0,
		Category: cat,
		Page:     page,
		OwnerID:  ctx.SenderID(),
		ChatID:   ctx.ChatID(),
	}

	useButtons, err := p.service.ResolveBool(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), "ui", "inline_buttons")
	if err != nil {
		p.logger.Debug("settings: resolve inline button preference failed", zap.Error(err))
	}
	if useButtons {
		if ctx.IsAssistant() {
			return p.openAssistantSettings(ctx, state)
		}
		return p.openNativeSettings(ctx, state)
	}

	screen := p.renderScreen(ctx.Ctx, state)
	text, _ := screen.Render()
	return ctx.Reply(text)
}

func (p *Plugin) renderScreen(ctx context.Context, state MenuState) *ui.Screen {
	ns, key := state.GetTarget()
	if ns != "" && key != "" {
		return p.renderSettingDetailScreen(ctx, state)
	}
	if state.Category != "" {
		return p.renderCategoryScreen(ctx, state)
	}
	return p.renderHomeScreen(ctx, state)
}

func settingsCategoryLabel(cat string) string {
	switch cat {
	case settings.CategoryGeneral:
		return "⚙️ General"
	case settings.CategorySecurity:
		return "🛡 Security"
	case settings.CategoryModeration:
		return "👮 Moderation"
	case settings.CategoryAutomation:
		return "⚡ Automation"
	case settings.CategoryUI:
		return "🎨 UI Layout"
	case settings.CategoryAdvanced:
		return "🔧 Advanced"
	default:
		return fmt.Sprintf("📁 %s", strings.Title(cat))
	}
}

func (p *Plugin) renderHomeScreen(_ context.Context, state MenuState) *ui.Screen {
	screen := ui.NewScreen("settings:home", "⚙️ GoUltroid Settings Dashboard",
		"Welcome to the interactive configuration dashboard.\nSelect a category below to view and modify settings:\n")
	var body strings.Builder
	body.WriteString("Available settings categories:\n\n")
	for _, cat := range p.service.Registry().Categories() {
		body.WriteString(fmt.Sprintf("• %s — <code>%s</code>\n", ui.EscapeHTML(settingsCategoryLabel(cat)), ui.EscapeHTML(cat)))
	}
	body.WriteString("\n<i>Inline buttons are disabled. Pass a category name to the settings command to browse its values.</i>")
	screen.Body = body.String()
	return screen
}

func (p *Plugin) renderCategoryScreen(ctx context.Context, state MenuState) *ui.Screen {
	defs := p.service.Registry().ListByCategory(state.Category)
	title := fmt.Sprintf("⚙️ Settings: %s", strings.Title(state.Category))
	if len(defs) == 0 {
		return ui.NewScreen("settings:cat", title, "No settings configured for this category.")
	}

	pageSize := 5
	pagedDefs, totalPages := ui.PaginateSlice(defs, state.Page, pageSize)
	if state.Page < 1 {
		state.Page = 1
	} else if state.Page > totalPages {
		state.Page = totalPages
	}

	var body strings.Builder
	body.WriteString(fmt.Sprintf("Category: <b>%s</b> | Scope: <b>%s</b>\n\n",
		ui.EscapeHTML(strings.Title(state.Category)), ui.EscapeHTML(string(state.Scope))))
	for _, def := range pagedDefs {
		currentVal, _ := p.service.Resolve(ctx, state.OwnerID, state.ScopeID, def.Namespace, def.Key)
		body.WriteString(fmt.Sprintf("• <b>%s</b> (<code>%s:%s</code>)\n  Val: <code>%s</code> | <i>%s</i>\n",
			ui.EscapeHTML(def.Title), ui.EscapeHTML(def.Namespace), ui.EscapeHTML(def.Key),
			ui.EscapeHTML(currentVal), ui.EscapeHTML(def.Description)))
	}
	if totalPages > 1 {
		body.WriteString(fmt.Sprintf("\nPage <b>%d / %d</b>. Pass a page number as the second argument to browse more settings.\n", state.Page, totalPages))
	}
	body.WriteString("\n<i>Inline buttons are disabled. Use the config command to change values.</i>")
	return ui.NewScreen("settings:cat", title, body.String())
}

func (p *Plugin) renderSettingDetailScreen(ctx context.Context, state MenuState) *ui.Screen {
	ns, key := state.GetTarget()
	if ns == "" || key == "" {
		state.Target = nil
		state.Selected = ""
		return p.renderCategoryScreen(ctx, state)
	}
	def, ok := p.service.Registry().Get(ns, key)
	if !ok {
		state.Target = nil
		state.Selected = ""
		return p.renderCategoryScreen(ctx, state)
	}
	currentVal, _ := p.service.Resolve(ctx, state.OwnerID, state.ScopeID, ns, key)
	originBadge := p.settingOriginBadge(ctx, state, ns, key)
	body := fmt.Sprintf(
		"<b>%s</b>\n%s\n\n<b>Type:</b> <code>%s</code>\n<b>Current Value:</b> <code>%s</code>\n<b>Origin:</b> %s\n<b>Default:</b> <code>%s</code>\n<b>Scope:</b> <code>%s</code>\n",
		ui.EscapeHTML(def.Title), ui.EscapeHTML(def.Description), def.Type,
		ui.EscapeHTML(currentVal), originBadge, ui.EscapeHTML(def.DefaultValue), state.Scope,
	)
	return ui.NewScreen("settings:detail", fmt.Sprintf("⚙️ %s (%s:%s)", def.Title, ns, key), body)
}

func (p *Plugin) settingOriginBadge(ctx context.Context, state MenuState, ns, key string) string {
	if state.Scope == settings.ScopeChat && state.ScopeID != 0 {
		if explicit, _ := p.service.Get(ctx, settings.ScopeChat, state.ScopeID, ns, key); explicit != nil {
			return "💬 Chat Override"
		}
	}
	if state.Scope == settings.ScopeUser || state.OwnerID != 0 {
		if explicit, _ := p.service.Get(ctx, settings.ScopeUser, state.OwnerID, ns, key); explicit != nil {
			return "👤 User Override"
		}
	}
	if explicit, _ := p.service.Get(ctx, settings.ScopeGlobal, 0, ns, key); explicit != nil {
		if state.Scope != settings.ScopeGlobal {
			return "🌐 Inherited from Global"
		}
		return "🌐 Global Setting"
	}
	return "⚙️ Schema Default"
}

func (p *Plugin) nextScope(state MenuState) (settings.SettingScope, int64, string) {
	if state.Scope == settings.ScopeGlobal {
		return settings.ScopeChat, state.ChatID, "Scope: 🌐 Global [Click to Chat]"
	}
	if state.Scope == settings.ScopeChat {
		return settings.ScopeUser, state.OwnerID, "Scope: 💬 Chat [Click to User]"
	}
	return settings.ScopeGlobal, 0, "Scope: 👤 User [Click to Global]"
}

// applySettingMutation is the transport-neutral settings mutation boundary used
// by both native and Assistant a2 interaction adapters.
func (p *Plugin) applySettingMutation(ctx context.Context, actorID int64, state *MenuState, action string) error {
	if p.setUC == nil {
		p.setUC = &usecase.SetSettingUseCase{Service: p.service}
	}
	if p.resetUC == nil {
		p.resetUC = &usecase.ResetSettingUseCase{Service: p.service}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ns, key := state.GetTarget()
	if ns == "" || key == "" {
		return nil
	}
	if err := state.ValidateScope(); err != nil {
		return err
	}

	switch action {
	case nativeIntentToggle:
		currentVal, err := p.service.Resolve(ctx, state.OwnerID, state.ScopeID, ns, key)
		if err != nil {
			return err
		}
		newVal := "true"
		if strings.EqualFold(currentVal, "true") {
			newVal = "false"
		}
		return p.setUC.Execute(ctx, state.Scope, state.ScopeID, ns, key, newVal, actorID)

	case nativeIntentSet:
		targetVal := state.ActionValue
		if targetVal == "" {
			parts := strings.Split(state.Selected, ":")
			if len(parts) == 3 {
				targetVal = parts[2]
			}
		}
		if targetVal == "" {
			return nil
		}
		if err := p.setUC.Execute(ctx, state.Scope, state.ScopeID, ns, key, targetVal, actorID); err != nil {
			return err
		}
		state.SetTarget(ns, key)
		return nil

	case nativeIntentReset:
		return p.resetUC.Execute(ctx, state.Scope, state.ScopeID, ns, key, actorID)
	}
	return nil
}

// =================== CLI .config Command Handler ===================

func (p *Plugin) handleConfigCommand(ctx *core.Context) error {
	if p.setUC == nil {
		p.setUC = &usecase.SetSettingUseCase{Service: p.service}
	}
	if p.resetUC == nil {
		p.resetUC = &usecase.ResetSettingUseCase{Service: p.service}
	}
	if len(ctx.Args) == 0 {
		return ctx.Result("⚙️ <b>GoUltroid CLI Configuration Subsystem</b>\n\n" +
			"<b>Usage:</b>\n" +
			"• <code>.config get &lt;namespace:key&gt;</code>\n" +
			"• <code>.config set &lt;namespace:key&gt; &lt;value&gt;</code>\n" +
			"• <code>.config reset &lt;namespace:key&gt;</code>\n" +
			"• <code>.config list [category]</code>\n" +
			"• <code>.config history &lt;namespace:key&gt;</code>\n" +
			"• <code>.config export</code>\n")
	}

	action := strings.ToLower(ctx.Args[0])
	switch action {
	case "get":
		if len(ctx.Args) < 2 {
			return ctx.Status("Usage: <code>.config get &lt;namespace:key&gt;</code>")
		}
		ns, key := parseFullKey(ctx.Args[1])
		val, err := p.service.Resolve(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), ns, key)
		if err != nil {
			return ctx.Error(ui.EscapeHTML(err.Error()))
		}
		return ctx.Result(fmt.Sprintf("⚙️ <b>%s:%s</b> = <code>%s</code>", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(val)))

	case "set":
		if len(ctx.Args) < 3 {
			return ctx.Status("Usage: <code>.config set &lt;namespace:key&gt; &lt;value&gt;</code>")
		}
		ns, key := parseFullKey(ctx.Args[1])
		val := strings.Join(ctx.Args[2:], " ")
		scope := settings.ScopeGlobal
		scopeID := int64(0)

		if err := p.setUC.Execute(ctx.Ctx, scope, scopeID, ns, key, val, ctx.SenderID()); err != nil {
			return ctx.Error(fmt.Sprintf("Failed to set <b>%s:%s</b>: %s", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(err.Error())))
		}
		return ctx.Success(fmt.Sprintf("Setting updated:\n<code>%s:%s</code> = <code>%s</code>", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(val)))

	case "reset":
		if len(ctx.Args) < 2 {
			return ctx.Status("Usage: <code>.config reset &lt;namespace:key&gt;</code>")
		}
		ns, key := parseFullKey(ctx.Args[1])
		if err := p.resetUC.Execute(ctx.Ctx, settings.ScopeGlobal, 0, ns, key, ctx.SenderID()); err != nil {
			return ctx.Error(fmt.Sprintf("Failed to reset <b>%s:%s</b>: %s", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(err.Error())))
		}
		return ctx.Success(fmt.Sprintf("Setting <code>%s:%s</code> reset to default.", ui.EscapeHTML(ns), ui.EscapeHTML(key)))

	case "list":
		cat := ""
		if len(ctx.Args) > 1 {
			cat = strings.ToLower(ctx.Args[1])
		}
		var defs []settings.SettingDefinition
		if cat != "" {
			defs = p.service.Registry().ListByCategory(cat)
		} else {
			defs = p.service.Registry().ListAll()
		}

		if len(defs) == 0 {
			return ctx.Status("No settings found.")
		}

		var sb strings.Builder
		sb.WriteString("📋 <b>GoUltroid Configuration Schema</b>\n\n")
		for _, d := range defs {
			cur, _ := p.service.Resolve(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), d.Namespace, d.Key)
			sb.WriteString(fmt.Sprintf("• <code>%s:%s</code> = <code>%s</code> (default: <code>%s</code>) [%s]\n  <i>%s</i>\n",
				ui.EscapeHTML(d.Namespace), ui.EscapeHTML(d.Key), ui.EscapeHTML(cur), ui.EscapeHTML(d.DefaultValue), ui.EscapeHTML(string(d.Type)), ui.EscapeHTML(d.Description)))
		}
		return ctx.Result(sb.String())

	case "history":
		if len(ctx.Args) < 2 {
			return ctx.Status("Usage: <code>.config history &lt;namespace:key&gt;</code>")
		}
		ns, key := parseFullKey(ctx.Args[1])
		history, err := p.service.GetHistory(ctx.Ctx, ns, key, 10)
		if err != nil {
			return ctx.Error("Error retrieving history: " + ui.EscapeHTML(err.Error()))
		}
		if len(history) == 0 {
			return ctx.Status(fmt.Sprintf("No change history found for <code>%s:%s</code>.", ui.EscapeHTML(ns), ui.EscapeHTML(key)))
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📜 <b>Change History for</b> <code>%s:%s</code>\n\n", ui.EscapeHTML(ns), ui.EscapeHTML(key)))
		for _, h := range history {
			sb.WriteString(fmt.Sprintf("• <code>%s</code> ➔ <code>%s</code> by user <code>%d</code> at <code>%s</code>\n",
				ui.EscapeHTML(h.OldVal), ui.EscapeHTML(h.NewVal), h.ChangedBy, h.ChangedAt.Format("2006-01-02 15:04:05")))
		}
		return ctx.Result(sb.String())

	case "export":
		exportData, err := p.service.Export(ctx.Ctx, settings.ScopeGlobal, 0)
		if err != nil {
			return ctx.Error("Export failed: " + ui.EscapeHTML(err.Error()))
		}
		bytes, _ := json.MarshalIndent(exportData, "", "  ")
		return ctx.Result(fmt.Sprintf("📤 <b>Global Settings Export:</b>\n<pre><code class=\"language-json\">%s</code></pre>", ui.EscapeHTML(string(bytes))))

	default:
		return ctx.Status(fmt.Sprintf("Unknown action <code>%s</code>. Use <code>.config</code> to see available commands.", ui.EscapeHTML(action)))
	}
}

func parseFullKey(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, ":") {
		parts := strings.SplitN(raw, ":", 2)
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	// Default namespace fallback
	return "core", raw
}
