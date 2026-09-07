package settings

import (
	"context"
	"encoding/json"
	"errors"
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

// SettingTarget specifies the exact namespace and key being inspected or modified.
type SettingTarget struct {
	Namespace string `json:"ns"`
	Key       string `json:"k"`
}

// MenuState captures interactive dashboard state stored in StateStore.
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

// EffectiveResolveIDs returns the appropriate user and chat IDs for resolution
// given the active menu scope.
func (s MenuState) EffectiveResolveIDs() (userID int64, chatID int64) {
	switch s.Scope {
	case settings.ScopeChat:
		return s.OwnerID, s.ScopeID
	case settings.ScopeUser:
		return s.ScopeID, 0
	default:
		return 0, 0
	}
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
		ChatID:   ctx.ChatID(),
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
	ns, key := state.GetTarget()
	if ns != "" && key != "" {
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
	catIcons := map[string]string{
		settings.CategoryGeneral:    "⚙️ General",
		settings.CategorySecurity:   "🛡 Security",
		settings.CategoryModeration: "👮 Moderation",
		settings.CategoryAutomation: "⚡ Automation",
		settings.CategoryUI:         "🎨 UI Layout",
		settings.CategoryAdvanced:   "🔧 Advanced",
	}

	var row []ui.Button
	for i, cat := range categories {
		label := catIcons[cat]
		if label == "" {
			label = fmt.Sprintf("📁 %s", strings.Title(cat))
		}
		catState := state
		catState.Category = cat
		catState.Page = 1
		catState.Target = nil
		catState.Selected = ""
		catOid := p.storeState(catState)

		btn := ui.NewCallbackButton(label, callback.EncodeCallbackData("settings", "nav", catOid))
		row = append(row, btn)

		if (i+1)%2 == 0 || i == len(categories)-1 {
			screen.AddRow(row...)
			row = nil
		}
	}
	if len(row) > 0 {
		screen.AddRow(row...)
	}

	// Scope switcher button
	nextScope := settings.ScopeGlobal
	var nextScopeID int64 = 0
	scopeText := "Scope: 🌐 Global"
	if state.Scope == settings.ScopeGlobal {
		if state.ChatID != 0 {
			nextScope = settings.ScopeChat
			nextScopeID = state.ChatID
			scopeText = "Scope: 🌐 Global [Click to Chat]"
		} else if state.OwnerID != 0 {
			nextScope = settings.ScopeUser
			nextScopeID = state.OwnerID
			scopeText = "Scope: 🌐 Global [Click to User]"
		} else {
			nextScope = settings.ScopeGlobal
			nextScopeID = 0
			scopeText = "Scope: 🌐 Global"
		}
	} else if state.Scope == settings.ScopeChat {
		if state.OwnerID != 0 {
			nextScope = settings.ScopeUser
			nextScopeID = state.OwnerID
			scopeText = "Scope: 💬 Chat [Click to User]"
		} else {
			nextScope = settings.ScopeGlobal
			nextScopeID = 0
			scopeText = "Scope: 💬 Chat [Click to Global]"
		}
	} else {
		nextScope = settings.ScopeGlobal
		nextScopeID = 0
		scopeText = "Scope: 👤 User [Click to Global]"
	}

	scopeState := state
	scopeState.Scope = nextScope
	scopeState.ScopeID = nextScopeID
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
	sb.WriteString(fmt.Sprintf("Category: <b>%s</b> | Scope: <b>%s</b>\n\n", ui.EscapeHTML(strings.Title(state.Category)), ui.EscapeHTML(string(state.Scope))))

	screen := ui.NewScreen("settings:cat", title, "")

	for _, def := range pagedDefs {
		// Resolve current value in scope
		uID, cID := state.EffectiveResolveIDs()
		currentVal, _ := p.service.Resolve(ctx, uID, cID, def.Namespace, def.Key)
		sb.WriteString(fmt.Sprintf("• <b>%s</b> (<code>%s:%s</code>)\n  Val: <code>%s</code> | <i>%s</i>\n",
			ui.EscapeHTML(def.Title), ui.EscapeHTML(def.Namespace), ui.EscapeHTML(def.Key), ui.EscapeHTML(currentVal), ui.EscapeHTML(def.Description)))

		switch def.Type {
		case settings.TypeBool:
			boolVal := strings.ToLower(currentVal) == "true"
			toggleState := state
			toggleState.SetTarget(def.Namespace, def.Key)
			toggleOid := p.storeState(toggleState)

			btn := ui.BuildToggleSwitch(boolVal, def.Title, def.Title, callback.EncodeCallbackData("settings", "toggle", toggleOid))
			screen.AddRow(btn)

		default:
			editState := state
			editState.SetTarget(def.Namespace, def.Key)
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
	homeState.Target = nil
	homeState.ActionValue = ""
	homeOid := p.storeState(homeState)

	closeOid := p.storeState(state)

	screen.AddRow(
		ui.NewCallbackButton("🏠 Home", callback.EncodeCallbackData("settings", "nav", homeOid)),
		ui.NewCallbackButton("❌ Close", callback.EncodeCallbackData("settings", "close", closeOid)),
	)

	return screen
}

func (p *Plugin) renderSettingDetailScreen(ctx context.Context, state MenuState) *ui.Screen {
	ns, key := state.GetTarget()
	if ns == "" || key == "" {
		state.Selected = ""
		state.Target = nil
		return p.renderCategoryScreen(ctx, state)
	}
	def, ok := p.service.Registry().Get(ns, key)
	if !ok {
		state.Selected = ""
		state.Target = nil
		return p.renderCategoryScreen(ctx, state)
	}

	uID, cID := state.EffectiveResolveIDs()
	currentVal, _ := p.service.Resolve(ctx, uID, cID, ns, key)
	title := fmt.Sprintf("⚙️ %s (%s:%s)", def.Title, ns, key)

	// Determine inheritance source
	var originBadge string
	if state.Scope == settings.ScopeChat && state.ScopeID != 0 {
		if explicit, _ := p.service.Get(ctx, settings.ScopeChat, state.ScopeID, ns, key); explicit != nil {
			originBadge = "💬 Chat Override"
		}
	}
	if originBadge == "" && state.Scope != settings.ScopeGlobal && state.OwnerID != 0 {
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
		"<b>%s</b>\n%s\n\n<b>Type:</b> <code>%s</code>\n<b>Current Value:</b> <code>%s</code>\n<b>Origin:</b> %s\n<b>Default:</b> <code>%s</code>\n<b>Scope:</b> <code>%s</code>\n",
		ui.EscapeHTML(def.Title), ui.EscapeHTML(def.Description), def.Type, ui.EscapeHTML(currentVal), originBadge, ui.EscapeHTML(def.DefaultValue), state.Scope,
	)

	screen := ui.NewScreen("settings:detail", title, body)
	noopData := callback.EncodeCallbackData("settings", "noop", "noop")

	switch def.Type {
	case settings.TypeBool:
		boolVal := strings.ToLower(currentVal) == "true"
		toggleState := state
		toggleState.SetTarget(ns, key)
		toggleOid := p.storeState(toggleState)
		screen.AddRow(ui.BuildStateToggle(boolVal, def.Title, callback.EncodeCallbackData("settings", "toggle", toggleOid)))

	case settings.TypeInt:
		intVal, _ := strconv.ParseInt(currentVal, 10, 64)
		decState := state
		decState.SetTarget(ns, key)
		decState.ActionValue = fmt.Sprintf("%d", intVal-1)
		decState.Selected = fmt.Sprintf("%s:%s:%d", ns, key, intVal-1)
		decOid := p.storeState(decState)

		incState := state
		incState.SetTarget(ns, key)
		incState.ActionValue = fmt.Sprintf("%d", intVal+1)
		incState.Selected = fmt.Sprintf("%s:%s:%d", ns, key, intVal+1)
		incOid := p.storeState(incState)

		decData := callback.EncodeCallbackData("settings", "step", decOid)
		incData := callback.EncodeCallbackData("settings", "step", incOid)

		screen.AddRow(ui.BuildStepper(intVal, def.MinVal, def.MaxVal, decData, incData, noopData)...)

	case settings.TypeDuration:
		durVal, _ := time.ParseDuration(currentVal)
		durRows := ui.BuildDurationPicker(nil, durVal, func(preset time.Duration) []byte {
			durState := state
			durState.SetTarget(ns, key)
			durState.ActionValue = preset.String()
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
			selState.SetTarget(ns, key)
			selState.ActionValue = opt
			selState.Selected = fmt.Sprintf("%s:%s:%s", ns, key, opt)
			oid := p.storeState(selState)
			return callback.EncodeCallbackData("settings", "select", oid)
		})
		screen.AddRow(selRow...)
	}

	// Reset button
	resetState := state
	resetState.SetTarget(ns, key)
	resetOid := p.storeState(resetState)
	resetLabel := "🔄 Reset to Default"
	if originBadge != "⚙️ Schema Default" && originBadge != "🌐 Global Setting" {
		resetLabel = "↩ Reset Override"
	}
	screen.AddRow(ui.NewCallbackButton(resetLabel, callback.EncodeCallbackData("settings", "reset", resetOid)))

	// Back button to category
	catState := state
	catState.Target = nil
	catState.Selected = ""
	catState.ActionValue = ""
	catOid := p.storeState(catState)
	screen.AddRow(ui.NewCallbackButton("🔙 Back to "+strings.Title(def.Category), callback.EncodeCallbackData("settings", "nav", catOid)))

	return screen
}

func (p *Plugin) storeState(st MenuState) string {
	return p.stateStore.Store(st, st.OwnerID, 15*time.Minute)
}

// applySettingAction encapsulates setting mutation logic (toggle, step, dur, select, reset)
// for both typed Target and backward-compatible Selected state formats.
func (p *Plugin) applySettingAction(ctx *callback.CallbackContext, state *MenuState, action string) error {
	ns, key := state.GetTarget()
	if ns == "" || key == "" {
		return nil
	}

	switch action {
	case "toggle":
		uID, cID := state.EffectiveResolveIDs()
		currentVal, _ := p.service.Resolve(ctx.Ctx, uID, cID, ns, key)
		newVal := "true"
		if strings.ToLower(currentVal) == "true" {
			newVal = "false"
		}
		if err := p.service.Set(ctx.Ctx, state.Scope, state.ScopeID, ns, key, newVal, ctx.UserID); err != nil {
			return ui.AnswerErrorToast(ctx, "Failed: "+err.Error())
		}
	case "step", "dur", "select", "set":
		targetVal := state.ActionValue
		if targetVal == "" {
			parts := strings.Split(state.Selected, ":")
			if len(parts) == 3 {
				targetVal = parts[2]
			}
		}
		if targetVal != "" {
			if err := p.service.Set(ctx.Ctx, state.Scope, state.ScopeID, ns, key, targetVal, ctx.UserID); err != nil {
				return ui.AnswerErrorToast(ctx, "Failed: "+err.Error())
			}
			state.SetTarget(ns, key)
		}
	case "reset":
		if err := p.service.Reset(ctx.Ctx, state.Scope, state.ScopeID, ns, key, ctx.UserID); err != nil {
			return ui.AnswerErrorToast(ctx, "Reset failed: "+err.Error())
		}
	}
	return nil
}

// =================== Callback Query Router Handler ===================

func (p *Plugin) HandleCallback(ctx *callback.CallbackContext) error {
	var state MenuState
	if ctx.State != nil {
		if s, ok := ctx.State.(MenuState); ok {
			state = s
		}
	}
	if state.ChatID == 0 && ctx.ChatID != 0 {
		state.ChatID = ctx.ChatID
	}
	if state.OwnerID == 0 && ctx.UserID != 0 {
		state.OwnerID = ctx.UserID
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

	case "toggle", "step", "dur", "select", "reset":
		if err := p.applySettingAction(ctx, &state, ctx.Action); err != nil {
			return err
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
		return ctx.Reply("⚙️ <b>GoUltroid CLI Configuration Subsystem</b>\n\n" +
			"<b>Usage:</b>\n" +
			"• <code>.config get [-s chat|user|global] &lt;namespace:key&gt;</code>\n" +
			"• <code>.config set [-s chat|user|global] &lt;namespace:key&gt; &lt;value&gt;</code>\n" +
			"• <code>.config reset [-s chat|user|global] &lt;namespace:key&gt;</code>\n" +
			"• <code>.config list [category]</code>\n" +
			"• <code>.config history &lt;namespace:key&gt;</code>\n" +
			"• <code>.config export [-s chat|user|global]</code>\n")
	}

	action := strings.ToLower(ctx.Args[0])
	switch action {
	case "get":
		scope, scopeID, remaining, err := parseScopeArgs(ctx.Args[1:], ctx)
		if err != nil {
			return ctx.Reply("❌ " + err.Error())
		}
		if len(remaining) < 1 {
			return ctx.Reply("⚠️ Usage: <code>.config get [-s chat|user|global] &lt;namespace:key&gt;</code>")
		}
		ns, key := parseFullKey(remaining[0])
		if len(ctx.Args) >= 3 && (ctx.Args[1] == "-s" || ctx.Args[1] == "--scope") {
			item, err := p.service.Get(ctx.Ctx, scope, scopeID, ns, key)
			if err != nil {
				return ctx.Reply(fmt.Sprintf("❌ <b>Error:</b> %s", ui.EscapeHTML(err.Error())))
			}
			if item == nil {
				return ctx.Reply(fmt.Sprintf("⚙️ <b>%s:%s</b> has no override in scope <code>%s</code>", ui.EscapeHTML(ns), ui.EscapeHTML(key), scope))
			}
			return ctx.Reply(fmt.Sprintf("⚙️ <b>%s:%s</b> (%s) = <code>%s</code>", ui.EscapeHTML(ns), ui.EscapeHTML(key), scope, ui.EscapeHTML(item.Value)))
		}
		val, err := p.service.Resolve(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), ns, key)
		if err != nil {
			return ctx.Reply(fmt.Sprintf("❌ <b>Error:</b> %s", ui.EscapeHTML(err.Error())))
		}
		return ctx.Reply(fmt.Sprintf("⚙️ <b>%s:%s</b> = <code>%s</code>", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(val)))

	case "set":
		scope, scopeID, remaining, err := parseScopeArgs(ctx.Args[1:], ctx)
		if err != nil {
			return ctx.Reply("❌ " + err.Error())
		}
		if len(remaining) < 2 {
			return ctx.Reply("⚠️ Usage: <code>.config set [-s chat|user|global] &lt;namespace:key&gt; &lt;value&gt;</code>")
		}
		ns, key := parseFullKey(remaining[0])
		val := strings.Join(remaining[1:], " ")

		if err := p.service.Set(ctx.Ctx, scope, scopeID, ns, key, val, ctx.SenderID()); err != nil {
			return ctx.Reply(fmt.Sprintf("❌ Failed to set <b>%s:%s</b>: %s", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(err.Error())))
		}
		return ctx.Reply(fmt.Sprintf("✅ <b>Setting updated (%s):</b>\n<code>%s:%s</code> = <code>%s</code>", scope, ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(val)))

	case "reset":
		scope, scopeID, remaining, err := parseScopeArgs(ctx.Args[1:], ctx)
		if err != nil {
			return ctx.Reply("❌ " + err.Error())
		}
		if len(remaining) < 1 {
			return ctx.Reply("⚠️ Usage: <code>.config reset [-s chat|user|global] &lt;namespace:key&gt;</code>")
		}
		ns, key := parseFullKey(remaining[0])
		if err := p.service.Reset(ctx.Ctx, scope, scopeID, ns, key, ctx.SenderID()); err != nil {
			return ctx.Reply(fmt.Sprintf("❌ Failed to reset <b>%s:%s</b>: %s", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(err.Error())))
		}
		if scope == settings.ScopeGlobal {
			return ctx.Reply(fmt.Sprintf("✅ Setting <code>%s:%s</code> reset to default.", ui.EscapeHTML(ns), ui.EscapeHTML(key)))
		}
		return ctx.Reply(fmt.Sprintf("✅ Setting <code>%s:%s</code> reset in scope <code>%s</code>.", ui.EscapeHTML(ns), ui.EscapeHTML(key), scope))

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
		sb.WriteString("📋 <b>GoUltroid Configuration Schema</b>\n\n")
		for _, d := range defs {
			cur, _ := p.service.Resolve(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), d.Namespace, d.Key)
			sb.WriteString(fmt.Sprintf("• <code>%s:%s</code> = <code>%s</code> (default: <code>%s</code>) [%s]\n  <i>%s</i>\n",
				ui.EscapeHTML(d.Namespace), ui.EscapeHTML(d.Key), ui.EscapeHTML(cur), ui.EscapeHTML(d.DefaultValue), ui.EscapeHTML(string(d.Type)), ui.EscapeHTML(d.Description)))
		}
		return ctx.Reply(sb.String())

	case "history":
		if len(ctx.Args) < 2 {
			return ctx.Reply("⚠️ Usage: <code>.config history &lt;namespace:key&gt;</code>")
		}
		ns, key := parseFullKey(ctx.Args[1])
		history, err := p.service.GetHistory(ctx.Ctx, ns, key, 10)
		if err != nil {
			return ctx.Reply(fmt.Sprintf("❌ <b>Error retrieving history:</b> %s", ui.EscapeHTML(err.Error())))
		}
		if len(history) == 0 {
			return ctx.Reply(fmt.Sprintf("No change history found for <code>%s:%s</code>.", ui.EscapeHTML(ns), ui.EscapeHTML(key)))
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📜 <b>Change History for</b> <code>%s:%s</code>\n\n", ui.EscapeHTML(ns), ui.EscapeHTML(key)))
		for _, h := range history {
			sb.WriteString(fmt.Sprintf("• <code>%s</code> ➔ <code>%s</code> by user <code>%d</code> at <code>%s</code>\n",
				ui.EscapeHTML(h.OldVal), ui.EscapeHTML(h.NewVal), h.ChangedBy, h.ChangedAt.Format("2006-01-02 15:04:05")))
		}
		return ctx.Reply(sb.String())

	case "export":
		scope, scopeID, _, err := parseScopeArgs(ctx.Args[1:], ctx)
		if err != nil {
			return ctx.Reply("❌ " + err.Error())
		}
		exportData, err := p.service.Export(ctx.Ctx, scope, scopeID)
		if err != nil {
			return ctx.Reply(fmt.Sprintf("❌ <b>Export failed:</b> %s", ui.EscapeHTML(err.Error())))
		}
		bytes, _ := json.MarshalIndent(exportData, "", "  ")
		title := "Global Settings Export"
		if scope != settings.ScopeGlobal {
			title = fmt.Sprintf("Settings Export (%s)", scope)
		}
		return ctx.Reply(fmt.Sprintf("📤 <b>%s:</b>\n<pre><code class=\"language-json\">%s</code></pre>", title, ui.EscapeHTML(string(bytes))))

	default:
		return ctx.Reply(fmt.Sprintf("⚠️ Unknown action <code>%s</code>. Use <code>.config</code> to see available commands.", ui.EscapeHTML(action)))
	}
}

func parseScopeArgs(args []string, ctx *core.Context) (settings.SettingScope, int64, []string, error) {
	scope := settings.ScopeGlobal
	scopeID := int64(0)
	remaining := args

	if len(remaining) >= 2 && (remaining[0] == "-s" || remaining[0] == "--scope") {
		sc, err := settings.NormalizeScope(remaining[1])
		if err != nil {
			return "", 0, nil, err
		}
		scope = sc
		remaining = remaining[2:]
		switch scope {
		case settings.ScopeChat:
			if ctx.ChatID() == 0 {
				return "", 0, nil, errors.New("chat scope requires a group or channel chat")
			}
			scopeID = ctx.ChatID()
		case settings.ScopeUser:
			if ctx.SenderID() == 0 {
				return "", 0, nil, errors.New("user scope requires a sender ID")
			}
			scopeID = ctx.SenderID()
		case settings.ScopeGlobal:
			scopeID = 0
		}
	}
	return scope, scopeID, remaining, nil
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
