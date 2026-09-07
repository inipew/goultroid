package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/ui"
	"go.uber.org/zap"
)

var (
	_ plugin.Plugin           = (*Plugin)(nil)
	_ callback.Handler        = (*Plugin)(nil)
	_ callback.HandlerWithOptions = (*Plugin)(nil)
)

// MenuState captures interactive dashboard state stored in StateStore.
type MenuState struct {
	Scope    settings.SettingScope `json:"sc"`
	ScopeID  int64                 `json:"sid"`
	Category string                `json:"cat"`
	Page     int                   `json:"p"`
	Selected string                `json:"sel,omitempty"` // ns:key
	OwnerID  int64                 `json:"ow"`
}

// Plugin provides interactive settings management via dashboard and CLI.
type Plugin struct {
	service    *settings.Service
	stateStore *callback.StateStore
	logger     *zap.Logger
	mu         sync.RWMutex
}

// New creates a new settings plugin instance.
func New(service *settings.Service, stateStore *callback.StateStore) *Plugin {
	if stateStore == nil {
		stateStore = callback.NewStateStore()
	}
	return &Plugin{
		service:    service,
		stateStore: stateStore,
		logger:     zap.NewNop(),
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

func (p *Plugin) Namespace() string {
	return "settings"
}

func (p *Plugin) CallbackOptions() callback.CallbackHandlerOptions {
	return callback.CallbackHandlerOptions{
		AutoAnswer: true,
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
			Handler:     p.handleSettingsCommand,
		},
		{
			Name:        "config",
			Description: "CLI command to inspect, set, or reset configuration",
			Usage:       ".config <get|set|reset|list|history|export> [args...]",
			Category:    "Settings",
			Permission:  core.PermissionOwner,
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

	state := MenuState{
		Scope:    settings.ScopeGlobal,
		ScopeID:  0,
		Category: cat,
		Page:     1,
		OwnerID:  ctx.SenderID(),
	}

	screen := p.renderScreen(ctx.Ctx, state)
	text, markup := screen.Render()

	// If interactive inline buttons enabled
	useButtons, _ := p.service.ResolveBool(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), "ui", "inline_buttons")
	if useButtons && markup != nil {
		return ctx.ReplyMarkup(text, markup)
	}
	return ctx.Reply(text)
}

func (p *Plugin) renderScreen(ctx context.Context, state MenuState) *ui.Screen {
	if state.Selected != "" {
		return p.renderSettingDetailScreen(ctx, state)
	}
	if state.Category != "" {
		return p.renderCategoryScreen(ctx, state)
	}
	return p.renderHomeScreen(ctx, state)
}

func (p *Plugin) renderHomeScreen(ctx context.Context, state MenuState) *ui.Screen {
	screen := ui.NewScreen("settings:home", "⚙️ GoUltroid Settings Dashboard",
		"Welcome to the interactive configuration dashboard.\nSelect a category below to view and modify settings:\n")

	categories := p.service.Registry().Categories()
	// Render 2 categories per row
	catIcons := map[string]string{
		settings.CategoryGeneral:    "⚙️ General",
		settings.CategorySecurity:   "🛡 Security",
		settings.CategoryModeration: "👮 Moderation",
		settings.CategoryAutomation: "⚡ Automation",
		settings.CategoryUI:         "🎨 UI Layout",
		settings.CategoryAdvanced:   "🔧 Advanced",
	}

	var row []ui.Button
	for _, c := range categories {
		label := catIcons[c]
		if label == "" {
			label = "📁 " + strings.Title(c)
		}

		nextState := state
		nextState.Category = c
		nextState.Page = 1
		oid := p.storeState(nextState)

		btn := ui.NewCallbackButton(label, callback.EncodeCallbackData("settings", "nav", oid))
		row = append(row, btn)
		if len(row) == 2 {
			screen.AddRow(row...)
			row = nil
		}
	}
	if len(row) > 0 {
		screen.AddRow(row...)
	}

	// Scope switcher button
	nextScope := settings.ScopeGlobal
	scopeText := "Scope: 🌐 Global"
	if state.Scope == settings.ScopeGlobal {
		nextScope = settings.ScopeChat
		scopeText = "Scope: 🌐 Global [Click to Chat]"
	} else if state.Scope == settings.ScopeChat {
		nextScope = settings.ScopeUser
		scopeText = "Scope: 💬 Chat [Click to User]"
	} else {
		nextScope = settings.ScopeGlobal
		scopeText = "Scope: 👤 User [Click to Global]"
	}

	scopeState := state
	scopeState.Scope = nextScope
	scopeOid := p.storeState(scopeState)

	closeOid := p.storeState(state)

	screen.AddRow(ui.NewCallbackButton(scopeText, callback.EncodeCallbackData("settings", "nav", scopeOid)))
	screen.AddRow(ui.NewCallbackButton("❌ Close", callback.EncodeCallbackData("settings", "close", closeOid)))

	return screen
}

func (p *Plugin) renderCategoryScreen(ctx context.Context, state MenuState) *ui.Screen {
	defs := p.service.Registry().ListByCategory(state.Category)
	title := fmt.Sprintf("⚙️ Settings: %s", strings.Title(state.Category))

	if len(defs) == 0 {
		screen := ui.NewScreen("settings:cat", title, "No settings configured for this category.")
		homeState := state
		homeState.Category = ""
		homeOid := p.storeState(homeState)
		screen.AddRow(ui.NewCallbackButton("🔙 Back", callback.EncodeCallbackData("settings", "nav", homeOid)))
		return screen
	}

	pageSize := 5
	pagedDefs, totalPages := ui.PaginateSlice(defs, state.Page, pageSize)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Category: **%s** | Scope: **%s**\n\n", strings.Title(state.Category), state.Scope))

	screen := ui.NewScreen("settings:cat", title, "")

	for _, def := range pagedDefs {
		// Resolve current value in scope
		currentVal, _ := p.service.Resolve(ctx, state.OwnerID, state.ScopeID, def.Namespace, def.Key)
		sb.WriteString(fmt.Sprintf("• **%s** (`%s:%s`)\n  Val: `%s` | _%s_\n", def.Title, def.Namespace, def.Key, currentVal, def.Description))

		fullKey := def.Namespace + ":" + def.Key
		switch def.Type {
		case settings.TypeBool:
			boolVal := strings.ToLower(currentVal) == "true"
			toggleState := state
			toggleState.Selected = fullKey
			toggleOid := p.storeState(toggleState)

			btn := ui.BuildToggleSwitch(boolVal, def.Title, def.Title, callback.EncodeCallbackData("settings", "toggle", toggleOid))
			screen.AddRow(btn)

		default:
			editState := state
			editState.Selected = fullKey
			editOid := p.storeState(editState)

			btn := ui.NewCallbackButton("⚙️ Edit "+def.Title, callback.EncodeCallbackData("settings", "nav", editOid))
			screen.AddRow(btn)
		}
	}

	screen.Body = sb.String()

	// Pagination row
	noopData := callback.EncodeCallbackData("settings", "noop", "noop")
	pagRow := ui.BuildPaginationRow(state.Page, totalPages, func(targetPage int) []byte {
		pState := state
		pState.Page = targetPage
		oid := p.storeState(pState)
		return callback.EncodeCallbackData("settings", "nav", oid)
	}, noopData)
	if len(pagRow) > 0 {
		screen.AddRow(pagRow...)
	}

	// Back to Home row
	homeState := state
	homeState.Category = ""
	homeState.Page = 1
	homeState.Selected = ""
	homeOid := p.storeState(homeState)

	closeOid := p.storeState(state)

	screen.AddRow(
		ui.NewCallbackButton("🏠 Home", callback.EncodeCallbackData("settings", "nav", homeOid)),
		ui.NewCallbackButton("❌ Close", callback.EncodeCallbackData("settings", "close", closeOid)),
	)

	return screen
}

func (p *Plugin) renderSettingDetailScreen(ctx context.Context, state MenuState) *ui.Screen {
	parts := strings.SplitN(state.Selected, ":", 2)
	if len(parts) != 2 {
		state.Selected = ""
		return p.renderCategoryScreen(ctx, state)
	}
	ns, key := parts[0], parts[1]
	def, ok := p.service.Registry().Get(ns, key)
	if !ok {
		state.Selected = ""
		return p.renderCategoryScreen(ctx, state)
	}

	currentVal, _ := p.service.Resolve(ctx, state.OwnerID, state.ScopeID, ns, key)
	title := fmt.Sprintf("⚙️ %s (%s:%s)", def.Title, ns, key)

	// Determine inheritance source
	var originBadge string
	if state.Scope == settings.ScopeChat && state.ScopeID != 0 {
		if explicit, _ := p.service.Get(ctx, settings.ScopeChat, state.ScopeID, ns, key); explicit != nil {
			originBadge = "💬 Chat Override"
		}
	}
	if originBadge == "" && (state.Scope == settings.ScopeUser || state.OwnerID != 0) {
		if explicit, _ := p.service.Get(ctx, settings.ScopeUser, state.OwnerID, ns, key); explicit != nil {
			originBadge = "👤 User Override"
		}
	}
	if originBadge == "" {
		if explicit, _ := p.service.Get(ctx, settings.ScopeGlobal, 0, ns, key); explicit != nil {
			if state.Scope != settings.ScopeGlobal {
				originBadge = "🌐 Inherited from Global"
			} else {
				originBadge = "🌐 Global Setting"
			}
		}
	}
	if originBadge == "" {
		originBadge = "⚙️ Schema Default"
	}

	body := fmt.Sprintf(
		"**%s**\n%s\n\n**Type:** `%s`\n**Current Value:** `%s`\n**Origin:** `%s`\n**Default:** `%s`\n**Scope:** `%s`\n",
		def.Title, def.Description, def.Type, currentVal, originBadge, def.DefaultValue, state.Scope,
	)

	screen := ui.NewScreen("settings:detail", title, body)
	noopData := callback.EncodeCallbackData("settings", "noop", "noop")

	switch def.Type {
	case settings.TypeBool:
		boolVal := strings.ToLower(currentVal) == "true"
		toggleOid := p.storeState(state)
		screen.AddRow(ui.BuildStateToggle(boolVal, def.Title, callback.EncodeCallbackData("settings", "toggle", toggleOid)))

	case settings.TypeInt:
		intVal, _ := strconv.ParseInt(currentVal, 10, 64)
		decState := state
		decState.Selected = fmt.Sprintf("%s:%s:%d", ns, key, intVal-1)
		decOid := p.storeState(decState)

		incState := state
		incState.Selected = fmt.Sprintf("%s:%s:%d", ns, key, intVal+1)
		incOid := p.storeState(incState)

		decData := callback.EncodeCallbackData("settings", "step", decOid)
		incData := callback.EncodeCallbackData("settings", "step", incOid)

		screen.AddRow(ui.BuildStepper(intVal, def.MinVal, def.MaxVal, decData, incData, noopData)...)

	case settings.TypeDuration:
		durVal, _ := time.ParseDuration(currentVal)
		durRows := ui.BuildDurationPicker(nil, durVal, func(preset time.Duration) []byte {
			durState := state
			durState.Selected = fmt.Sprintf("%s:%s:%s", ns, key, preset.String())
			oid := p.storeState(durState)
			return callback.EncodeCallbackData("settings", "dur", oid)
		})
		for _, r := range durRows {
			screen.AddRow(r...)
		}

	case settings.TypeEnum:
		selRow := ui.BuildSelector(def.AllowedValues, currentVal, func(opt string) []byte {
			selState := state
			selState.Selected = fmt.Sprintf("%s:%s:%s", ns, key, opt)
			oid := p.storeState(selState)
			return callback.EncodeCallbackData("settings", "select", oid)
		})
		screen.AddRow(selRow...)
	}

	// Reset button
	resetState := state
	resetOid := p.storeState(resetState)
	resetLabel := "🔄 Reset to Default"
	if originBadge != "⚙️ Schema Default" && originBadge != "🌐 Global Setting" {
		resetLabel = "↩ Reset Override"
	}
	screen.AddRow(ui.NewCallbackButton(resetLabel, callback.EncodeCallbackData("settings", "reset", resetOid)))

	// Back button to category
	catState := state
	catState.Selected = ""
	catOid := p.storeState(catState)
	screen.AddRow(ui.NewCallbackButton("🔙 Back to "+strings.Title(def.Category), callback.EncodeCallbackData("settings", "nav", catOid)))

	return screen
}

func (p *Plugin) storeState(st MenuState) string {
	return p.stateStore.Store(st, st.OwnerID, 15*time.Minute)
}

// =================== Callback Query Router Handler ===================

func (p *Plugin) HandleCallback(ctx *callback.CallbackContext) error {
	var state MenuState
	if ctx.State != nil {
		if s, ok := ctx.State.(MenuState); ok {
			state = s
		}
	}

	switch ctx.Action {
	case "close":
		return ctx.DisableButtons("✅ Settings dashboard closed.")

	case "noop":
		return nil

	case "nav":
		screen := p.renderScreen(ctx.Ctx, state)
		text, markup := screen.Render()
		return ctx.Edit(text, markup)

	case "toggle":
		parts := strings.SplitN(state.Selected, ":", 2)
		if len(parts) == 2 {
			ns, key := parts[0], parts[1]
			currentVal, _ := p.service.Resolve(ctx.Ctx, state.OwnerID, state.ScopeID, ns, key)
			newVal := "true"
			if strings.ToLower(currentVal) == "true" {
				newVal = "false"
			}
			if err := p.service.Set(ctx.Ctx, state.Scope, state.ScopeID, ns, key, newVal, ctx.UserID); err != nil {
				return ui.AnswerErrorToast(ctx, "Failed: "+err.Error())
			}
		}
		screen := p.renderScreen(ctx.Ctx, state)
		text, markup := screen.Render()
		return ctx.Edit(text, markup)

	case "step":
		parts := strings.Split(state.Selected, ":")
		if len(parts) == 3 {
			ns, key, targetVal := parts[0], parts[1], parts[2]
			if err := p.service.Set(ctx.Ctx, state.Scope, state.ScopeID, ns, key, targetVal, ctx.UserID); err != nil {
				return ui.AnswerErrorToast(ctx, "Failed: "+err.Error())
			}
			state.Selected = ns + ":" + key
		}
		screen := p.renderScreen(ctx.Ctx, state)
		text, markup := screen.Render()
		return ctx.Edit(text, markup)

	case "dur":
		parts := strings.Split(state.Selected, ":")
		if len(parts) == 3 {
			ns, key, targetDur := parts[0], parts[1], parts[2]
			if err := p.service.Set(ctx.Ctx, state.Scope, state.ScopeID, ns, key, targetDur, ctx.UserID); err != nil {
				return ui.AnswerErrorToast(ctx, "Failed: "+err.Error())
			}
			state.Selected = ns + ":" + key
		}
		screen := p.renderScreen(ctx.Ctx, state)
		text, markup := screen.Render()
		return ctx.Edit(text, markup)

	case "select":
		parts := strings.Split(state.Selected, ":")
		if len(parts) == 3 {
			ns, key, targetOpt := parts[0], parts[1], parts[2]
			if err := p.service.Set(ctx.Ctx, state.Scope, state.ScopeID, ns, key, targetOpt, ctx.UserID); err != nil {
				return ui.AnswerErrorToast(ctx, "Failed: "+err.Error())
			}
			state.Selected = ns + ":" + key
		}
		screen := p.renderScreen(ctx.Ctx, state)
		text, markup := screen.Render()
		return ctx.Edit(text, markup)

	case "reset":
		parts := strings.SplitN(state.Selected, ":", 2)
		if len(parts) == 2 {
			ns, key := parts[0], parts[1]
			if err := p.service.Reset(ctx.Ctx, state.Scope, state.ScopeID, ns, key); err != nil {
				return ui.AnswerErrorToast(ctx, "Reset failed: "+err.Error())
			}
		}
		screen := p.renderScreen(ctx.Ctx, state)
		text, markup := screen.Render()
		return ctx.Edit(text, markup)

	default:
		return fmt.Errorf("unknown settings callback action: %s", ctx.Action)
	}
}

// =================== CLI .config Command Handler ===================

func (p *Plugin) handleConfigCommand(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.Reply("**GoUltroid CLI Configuration Subsystem**\n\n" +
			"Usage:\n" +
			"• `.config get <key>`\n" +
			"• `.config set <key> <value>`\n" +
			"• `.config reset <key>`\n" +
			"• `.config list [category]`\n" +
			"• `.config history <key>`\n" +
			"• `.config export`\n")
	}

	action := strings.ToLower(ctx.Args[0])
	switch action {
	case "get":
		if len(ctx.Args) < 2 {
			return ctx.Reply("⚠️ Usage: `.config get <namespace:key>`")
		}
		ns, key := parseFullKey(ctx.Args[1])
		val, err := p.service.Resolve(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), ns, key)
		if err != nil {
			return ctx.Reply(fmt.Sprintf("❌ Error: %v", err))
		}
		return ctx.Reply(fmt.Sprintf("⚙️ **%s:%s** = `%s`", ns, key, val))

	case "set":
		if len(ctx.Args) < 3 {
			return ctx.Reply("⚠️ Usage: `.config set <namespace:key> <value>`")
		}
		ns, key := parseFullKey(ctx.Args[1])
		val := strings.Join(ctx.Args[2:], " ")
		scope := settings.ScopeGlobal
		scopeID := int64(0)

		if err := p.service.Set(ctx.Ctx, scope, scopeID, ns, key, val, ctx.SenderID()); err != nil {
			return ctx.Reply(fmt.Sprintf("❌ Failed to set **%s:%s**: %v", ns, key, err))
		}
		return ctx.Reply(fmt.Sprintf("✅ Setting updated:\n**%s:%s** = `%s`", ns, key, val))

	case "reset":
		if len(ctx.Args) < 2 {
			return ctx.Reply("⚠️ Usage: `.config reset <namespace:key>`")
		}
		ns, key := parseFullKey(ctx.Args[1])
		if err := p.service.Reset(ctx.Ctx, settings.ScopeGlobal, 0, ns, key); err != nil {
			return ctx.Reply(fmt.Sprintf("❌ Failed to reset **%s:%s**: %v", ns, key, err))
		}
		return ctx.Reply(fmt.Sprintf("✅ Setting **%s:%s** reset to default.", ns, key))

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
			return ctx.Reply("No settings found.")
		}

		var sb strings.Builder
		sb.WriteString("📋 **GoUltroid Configuration Schema**\n\n")
		for _, d := range defs {
			cur, _ := p.service.Resolve(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), d.Namespace, d.Key)
			sb.WriteString(fmt.Sprintf("• `%s:%s` = `%s` (default: `%s`) [%s]\n  _%s_\n",
				d.Namespace, d.Key, cur, d.DefaultValue, d.Type, d.Description))
		}
		return ctx.Reply(sb.String())

	case "history":
		if len(ctx.Args) < 2 {
			return ctx.Reply("⚠️ Usage: `.config history <namespace:key>`")
		}
		ns, key := parseFullKey(ctx.Args[1])
		history, err := p.service.GetHistory(ctx.Ctx, ns, key, 10)
		if err != nil {
			return ctx.Reply(fmt.Sprintf("❌ Error retrieving history: %v", err))
		}
		if len(history) == 0 {
			return ctx.Reply(fmt.Sprintf("No change history found for `%s:%s`.", ns, key))
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📜 **Change History for `%s:%s`**\n\n", ns, key))
		for _, h := range history {
			sb.WriteString(fmt.Sprintf("• `%s` ➔ `%s` by user `%d` at `%s`\n",
				h.OldVal, h.NewVal, h.ChangedBy, h.ChangedAt.Format("2006-01-02 15:04:05")))
		}
		return ctx.Reply(sb.String())

	case "export":
		exportData, err := p.service.Export(ctx.Ctx, settings.ScopeGlobal, 0)
		if err != nil {
			return ctx.Reply(fmt.Sprintf("❌ Export failed: %v", err))
		}
		bytes, _ := json.MarshalIndent(exportData, "", "  ")
		return ctx.Reply(fmt.Sprintf("📤 **Global Settings Export**:\n```json\n%s\n```", string(bytes)))

	default:
		return ctx.Reply(fmt.Sprintf("⚠️ Unknown action `%s`. Use `.config` to see available commands.", action))
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
