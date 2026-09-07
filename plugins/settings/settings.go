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
	"github.com/inipew/goultroid/internal/ui/render"
	"github.com/inipew/goultroid/plugins/settings/usecase"
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
	service    *settings.Service
	stateStore *callback.StateStore
	logger     *zap.Logger
	mu         sync.RWMutex
	setUC      *usecase.SetSettingUseCase
	resetUC    *usecase.ResetSettingUseCase
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
		setUC:      &usecase.SetSettingUseCase{Service: service},
		resetUC:    &usecase.ResetSettingUseCase{Service: service},
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
	tgMarkup := render.ToTelegramMarkup(markup)

	// If interactive inline buttons enabled
	useButtons, _ := p.service.ResolveBool(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), "ui", "inline_buttons")
	if useButtons && tgMarkup != nil {
		return ctx.ReplyMarkup(text, tgMarkup)
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

		btn := ui.NewCallbackButton(label, callback.EncodeCallbackData("settings", callback.ActionNav, catOid))
		row = append(row, btn)

		if (i+1)%2 == 0 || i == len(categories)-1 {
			screen.AddRow(row...)
			row = nil
		}
	}
	if len(row) > 0 {
		screen.AddRow(row...)
	}

	nextScope, nextScopeID, scopeText := p.nextScope(state)
	scopeState := state
	scopeState.Scope = nextScope
	scopeState.ScopeID = nextScopeID
	scopeOid := p.storeState(scopeState)

	closeOid := p.storeState(state)

	screen.AddRow(ui.NewCallbackButton(scopeText, callback.EncodeCallbackData("settings", callback.ActionNav, scopeOid)))
	screen.AddRow(ui.NewCallbackButton("❌ Close", callback.EncodeCallbackData("settings", callback.ActionClose, closeOid)))

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
		screen.AddRow(ui.NewCallbackButton("🔙 Back", callback.EncodeCallbackData("settings", callback.ActionNav, homeOid)))
		return screen
	}

	pageSize := 5
	pagedDefs, totalPages := ui.PaginateSlice(defs, state.Page, pageSize)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Category: <b>%s</b> | Scope: <b>%s</b>\n\n", ui.EscapeHTML(strings.Title(state.Category)), ui.EscapeHTML(string(state.Scope))))

	screen := ui.NewScreen("settings:cat", title, "")

	for _, def := range pagedDefs {
		// Resolve current value in scope
		currentVal, _ := p.service.Resolve(ctx, state.OwnerID, state.ScopeID, def.Namespace, def.Key)
		sb.WriteString(fmt.Sprintf("• <b>%s</b> (<code>%s:%s</code>)\n  Val: <code>%s</code> | <i>%s</i>\n",
			ui.EscapeHTML(def.Title), ui.EscapeHTML(def.Namespace), ui.EscapeHTML(def.Key), ui.EscapeHTML(currentVal), ui.EscapeHTML(def.Description)))

		switch def.Type {
		case settings.TypeBool:
			boolVal := strings.ToLower(currentVal) == "true"
			toggleState := state
			toggleState.SetTarget(def.Namespace, def.Key)
			toggleOid := p.storeState(toggleState)

			btn := ui.BuildToggleSwitch(boolVal, def.Title, def.Title, callback.EncodeCallbackData("settings", callback.ActionToggle, toggleOid))
			screen.AddRow(btn)

		default:
			editState := state
			editState.SetTarget(def.Namespace, def.Key)
			editOid := p.storeState(editState)

			btn := ui.NewCallbackButton("⚙️ Edit "+def.Title, callback.EncodeCallbackData("settings", callback.ActionNav, editOid))
			screen.AddRow(btn)
		}
	}

	screen.Body = sb.String()

	// Pagination row
	noopData := callback.EncodeCallbackData("settings", callback.ActionNoop, callback.ActionNoop)
	pagRow := ui.BuildPaginationRow(state.Page, totalPages, func(targetPage int) []byte {
		pState := state
		pState.Page = targetPage
		oid := p.storeState(pState)
		return callback.EncodeCallbackData("settings", callback.ActionNav, oid)
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
		ui.NewCallbackButton("🏠 Home", callback.EncodeCallbackData("settings", callback.ActionNav, homeOid)),
		ui.NewCallbackButton("❌ Close", callback.EncodeCallbackData("settings", callback.ActionClose, closeOid)),
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
	currentVal, _ := p.service.Resolve(ctx, state.OwnerID, state.ScopeID, ns, key)
	originBadge := p.settingOriginBadge(ctx, state, ns, key)
	body := fmt.Sprintf(
		"<b>%s</b>\n%s\n\n<b>Type:</b> <code>%s</code>\n<b>Current Value:</b> <code>%s</code>\n<b>Origin:</b> %s\n<b>Default:</b> <code>%s</code>\n<b>Scope:</b> <code>%s</code>\n",
		ui.EscapeHTML(def.Title), ui.EscapeHTML(def.Description), def.Type, ui.EscapeHTML(currentVal), originBadge, ui.EscapeHTML(def.DefaultValue), state.Scope,
	)
	screen := ui.NewScreen("settings:detail", fmt.Sprintf("⚙️ %s (%s:%s)", def.Title, ns, key), body)
	noopData := callback.EncodeCallbackData("settings", callback.ActionNoop, callback.ActionNoop)
	p.addSettingTypeControls(screen, state, ns, key, currentVal, *def, noopData)
	p.addDetailFooter(screen, state, ns, key, originBadge, def.Category)
	return screen
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

func (p *Plugin) addSettingTypeControls(screen *ui.Screen, state MenuState, ns, key, currentVal string, def settings.SettingDefinition, noopData []byte) {
	switch def.Type {
	case settings.TypeBool:
		boolVal := strings.ToLower(currentVal) == "true"
		toggleState := state
		toggleState.SetTarget(ns, key)
		toggleOid := p.storeState(toggleState)
		screen.AddRow(ui.BuildStateToggle(boolVal, def.Title, callback.EncodeCallbackData("settings", callback.ActionToggle, toggleOid)))
	case settings.TypeInt:
		intVal, _ := strconv.ParseInt(currentVal, 10, 64)
		decState := state
		decState.SetTarget(ns, key)
		decState.ActionValue = fmt.Sprintf("%d", intVal-1)
		decOid := p.storeState(decState)
		incState := state
		incState.SetTarget(ns, key)
		incState.ActionValue = fmt.Sprintf("%d", intVal+1)
		incOid := p.storeState(incState)
		decData := callback.EncodeCallbackData("settings", callback.ActionStep, decOid)
		incData := callback.EncodeCallbackData("settings", callback.ActionStep, incOid)
		screen.AddRow(ui.BuildStepper(intVal, def.MinVal, def.MaxVal, decData, incData, noopData)...)
	case settings.TypeDuration:
		durVal, _ := time.ParseDuration(currentVal)
		durRows := ui.BuildDurationPicker(nil, durVal, func(preset time.Duration) []byte {
			durState := state
			durState.SetTarget(ns, key)
			durState.ActionValue = preset.String()
			oid := p.storeState(durState)
			return callback.EncodeCallbackData("settings", callback.ActionDuration, oid)
		})
		for _, r := range durRows {
			screen.AddRow(r...)
		}
	case settings.TypeEnum:
		selRow := ui.BuildSelector(def.AllowedValues, currentVal, func(opt string) []byte {
			selState := state
			selState.SetTarget(ns, key)
			selState.ActionValue = opt
			oid := p.storeState(selState)
			return callback.EncodeCallbackData("settings", callback.ActionSelect, oid)
		})
		screen.AddRow(selRow...)
	}
}

func (p *Plugin) addDetailFooter(screen *ui.Screen, state MenuState, ns, key, originBadge, category string) {
	resetState := state
	resetState.SetTarget(ns, key)
	resetOid := p.storeState(resetState)
	resetLabel := "🔄 Reset to Default"
	if originBadge != "⚙️ Schema Default" && originBadge != "🌐 Global Setting" {
		resetLabel = "↩ Reset Override"
	}
	screen.AddRow(ui.NewCallbackButton(resetLabel, callback.EncodeCallbackData("settings", callback.ActionReset, resetOid)))
	catState := state
	catState.Target = nil
	catState.Selected = ""
	catState.ActionValue = ""
	catOid := p.storeState(catState)
	screen.AddRow(ui.NewCallbackButton("🔙 Back to "+strings.Title(category), callback.EncodeCallbackData("settings", callback.ActionNav, catOid)))
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

func (p *Plugin) storeState(st MenuState) string {
	return p.stateStore.Store(st, st.OwnerID, 15*time.Minute)
}

// applySettingAction encapsulates setting mutation logic (toggle, step, dur, select, reset)
// for both typed Target and backward-compatible Selected state formats.
// Uses use-case layer so command and callback share the same validation path.
func (p *Plugin) applySettingAction(ctx *callback.CallbackContext, state *MenuState, action string) error {
	if p.setUC == nil {
		p.setUC = &usecase.SetSettingUseCase{Service: p.service}
	}
	if p.resetUC == nil {
		p.resetUC = &usecase.ResetSettingUseCase{Service: p.service}
	}
	ns, key := state.GetTarget()
	if ns == "" || key == "" {
		return nil
	}

	switch action {
	case callback.ActionToggle:
		currentVal, _ := p.service.Resolve(ctx.Ctx, state.OwnerID, state.ScopeID, ns, key)
		newVal := "true"
		if strings.ToLower(currentVal) == "true" {
			newVal = "false"
		}
		if err := state.ValidateScope(); err != nil {
			return ui.AnswerErrorToast(ctx, "Invalid scope: "+err.Error())
		}
		if err := p.setUC.Execute(ctx.Ctx, state.Scope, state.ScopeID, ns, key, newVal, ctx.UserID); err != nil {
			return ui.AnswerErrorToast(ctx, ui.MapUserErrorMessage(err))
		}
	case callback.ActionStep, callback.ActionDuration, callback.ActionSelect, callback.ActionSet:
		if err := state.ValidateScope(); err != nil {
			return ui.AnswerErrorToast(ctx, "Invalid scope: "+err.Error())
		}
		targetVal := state.ActionValue
		if targetVal == "" {
			parts := strings.Split(state.Selected, ":")
			if len(parts) == 3 {
				targetVal = parts[2]
			}
		}
		if targetVal != "" {
			if err := p.setUC.Execute(ctx.Ctx, state.Scope, state.ScopeID, ns, key, targetVal, ctx.UserID); err != nil {
				return ui.AnswerErrorToast(ctx, ui.MapUserErrorMessage(err))
			}
			state.SetTarget(ns, key)
		}
	case callback.ActionReset:
		if err := p.resetUC.Execute(ctx.Ctx, state.Scope, state.ScopeID, ns, key, ctx.UserID); err != nil {
			return ui.AnswerErrorToast(ctx, ui.MapUserErrorMessage(err))
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
	case callback.ActionClose:
		return ctx.DisableButtons("✅ Settings dashboard closed.")

	case callback.ActionNoop:
		return nil

	case callback.ActionNav:
		screen := p.renderScreen(ctx.Ctx, state)
		text, markup := screen.Render()
		return ctx.Edit(text, render.ToTelegramMarkup(markup))

	case callback.ActionToggle, callback.ActionStep, callback.ActionDuration, callback.ActionSelect, callback.ActionReset:
		if err := p.applySettingAction(ctx, &state, ctx.Action); err != nil {
			return err
		}
		screen := p.renderScreen(ctx.Ctx, state)
		text, markup := screen.Render()
		return ctx.Edit(text, render.ToTelegramMarkup(markup))

	default:
		return fmt.Errorf("unknown settings callback action: %s", ctx.Action)
	}
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
		return ctx.Reply("⚙️ <b>GoUltroid CLI Configuration Subsystem</b>\n\n" +
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
			return ctx.Reply("⚠️ Usage: <code>.config get &lt;namespace:key&gt;</code>")
		}
		ns, key := parseFullKey(ctx.Args[1])
		val, err := p.service.Resolve(ctx.Ctx, ctx.SenderID(), ctx.ChatID(), ns, key)
		if err != nil {
			return ctx.Reply(fmt.Sprintf("❌ <b>Error:</b> %s", ui.EscapeHTML(err.Error())))
		}
		return ctx.Reply(fmt.Sprintf("⚙️ <b>%s:%s</b> = <code>%s</code>", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(val)))

	case "set":
		if len(ctx.Args) < 3 {
			return ctx.Reply("⚠️ Usage: <code>.config set &lt;namespace:key&gt; &lt;value&gt;</code>")
		}
		ns, key := parseFullKey(ctx.Args[1])
		val := strings.Join(ctx.Args[2:], " ")
		scope := settings.ScopeGlobal
		scopeID := int64(0)

		if err := p.setUC.Execute(ctx.Ctx, scope, scopeID, ns, key, val, ctx.SenderID()); err != nil {
			return ctx.Reply(fmt.Sprintf("❌ Failed to set <b>%s:%s</b>: %s", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(err.Error())))
		}
		return ctx.Reply(fmt.Sprintf("✅ <b>Setting updated:</b>\n<code>%s:%s</code> = <code>%s</code>", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(val)))

	case "reset":
		if len(ctx.Args) < 2 {
			return ctx.Reply("⚠️ Usage: <code>.config reset &lt;namespace:key&gt;</code>")
		}
		ns, key := parseFullKey(ctx.Args[1])
		if err := p.resetUC.Execute(ctx.Ctx, settings.ScopeGlobal, 0, ns, key, ctx.SenderID()); err != nil {
			return ctx.Reply(fmt.Sprintf("❌ Failed to reset <b>%s:%s</b>: %s", ui.EscapeHTML(ns), ui.EscapeHTML(key), ui.EscapeHTML(err.Error())))
		}
		return ctx.Reply(fmt.Sprintf("✅ Setting <code>%s:%s</code> reset to default.", ui.EscapeHTML(ns), ui.EscapeHTML(key)))

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
		exportData, err := p.service.Export(ctx.Ctx, settings.ScopeGlobal, 0)
		if err != nil {
			return ctx.Reply(fmt.Sprintf("❌ <b>Export failed:</b> %s", ui.EscapeHTML(err.Error())))
		}
		bytes, _ := json.MarshalIndent(exportData, "", "  ")
		return ctx.Reply(fmt.Sprintf("📤 <b>Global Settings Export:</b>\n<pre><code class=\"language-json\">%s</code></pre>", ui.EscapeHTML(string(bytes))))

	default:
		return ctx.Reply(fmt.Sprintf("⚠️ Unknown action <code>%s</code>. Use <code>.config</code> to see available commands.", ui.EscapeHTML(action)))
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
