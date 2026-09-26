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
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	settingssvc "github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/ui"
)

const (
	nativeSettingsTTL             = 15 * time.Minute
	nativeSettingsScreenDashboard = "dashboard"
	nativeSettingsActionSlotCount = 32
	nativeSettingsPageSize        = 5

	nativeIntentNoop     = "noop"
	nativeIntentClose    = "close"
	nativeIntentHome     = "home"
	nativeIntentCategory = "category"
	nativeIntentTarget   = "target"
	nativeIntentPage     = "page"
	nativeIntentScope    = "scope"
	nativeIntentBack     = "back"
	nativeIntentToggle   = "toggle"
	nativeIntentSet      = "set"
	nativeIntentReset    = "reset"
)

var nativeDurationPresets = []time.Duration{
	10 * time.Second,
	30 * time.Second,
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
	1 * time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

type nativeRuntimeState struct {
	mu      sync.RWMutex
	runtime nativeinteraction.DriverRuntime
}

type nativeSettingsState struct {
	Menu  MenuState              `json:"menu"`
	Slots []nativeSettingsIntent `json:"slots,omitempty"`
}

type nativeSettingsIntent struct {
	Kind      string                   `json:"k"`
	Category  string                   `json:"cat,omitempty"`
	Page      int                      `json:"p,omitempty"`
	Namespace string                   `json:"ns,omitempty"`
	Key       string                   `json:"key,omitempty"`
	Value     string                   `json:"v,omitempty"`
	Scope     settingssvc.SettingScope `json:"sc,omitempty"`
	ScopeID   int64                    `json:"sid,omitempty"`
}

func (p *Plugin) FeatureSpec() feature.Spec {
	policy := feature.OwnerPolicy(execution.SurfaceUserbot)
	interactions := []feature.Interaction{{
		ID:          nativeSettingsScreenDashboard,
		Kind:        feature.InteractionScreen,
		Description: "Native userbot settings dashboard",
		Surfaces:    execution.SurfaceUserbot,
		Policy:      policy,
	}}
	for i := 0; i < nativeSettingsActionSlotCount; i++ {
		interactions = append(interactions, feature.Interaction{
			ID:          nativeSettingsSlotID(i),
			Kind:        feature.InteractionAction,
			Description: "Native userbot settings session action slot",
			Surfaces:    execution.SurfaceUserbot,
			Policy:      policy,
		})
	}
	return feature.Spec{
		ID:           p.Name(),
		Name:         "Settings",
		Description:  p.Description(),
		Category:     "Settings",
		Interactions: interactions,
	}
}

func (p *Plugin) NativeFeatureID() string { return p.Name() }

func nativeSettingsSlotID(slot int) string {
	return fmt.Sprintf("slot_%02d", slot)
}

func (p *Plugin) BindNative(rt nativeinteraction.DriverRuntime) (func(), error) {
	if p == nil || rt.Interactions == nil || rt.Catalog == nil || rt.Scope.IsZero() {
		return nil, nativeinteraction.ErrUnavailable
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope != rt.Scope {
		return nil, fmt.Errorf("settings: native feature scope unavailable")
	}

	registrations := make([]interface{ Close() }, 0, nativeSettingsActionSlotCount)
	for i := 0; i < nativeSettingsActionSlotCount; i++ {
		slot := i
		actionID := nativeSettingsSlotID(slot)
		registration, err := rt.Interactions.RegisterAction(rt.Scope, p.Name(), actionID, func(ctx *orchestration.Context) error {
			return p.handleNativeSettingsSlot(ctx, slot)
		})
		if err != nil {
			for j := len(registrations) - 1; j >= 0; j-- {
				registrations[j].Close()
			}
			return nil, fmt.Errorf("settings: register native action %s: %w", actionID, err)
		}
		registrations = append(registrations, registration)
	}

	p.native.mu.Lock()
	p.native.runtime = rt
	p.native.mu.Unlock()

	cleanup := func() {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
		p.native.mu.Lock()
		if p.native.runtime.Interactions == rt.Interactions && p.native.runtime.Scope == rt.Scope {
			p.native.runtime = nativeinteraction.DriverRuntime{}
		}
		p.native.mu.Unlock()
	}
	return cleanup, nil
}

func (p *Plugin) currentNativeRuntime() nativeinteraction.DriverRuntime {
	if p == nil {
		return nativeinteraction.DriverRuntime{}
	}
	p.native.mu.RLock()
	rt := p.native.runtime
	p.native.mu.RUnlock()
	return rt
}

func (p *Plugin) openNativeSettings(cmd *core.Context, state MenuState) error {
	if p == nil || cmd == nil {
		return nativeinteraction.ErrInvalidInvocation
	}
	rt := p.currentNativeRuntime()
	if rt.Interactions == nil {
		return nativeinteraction.ErrUnavailable
	}
	raw, view, err := p.nativeSettingsView(cmd.Ctx, state)
	if err != nil {
		return err
	}
	_, err = rt.Interactions.Begin(cmd, nativeinteraction.BeginRequest{
		FeatureID: p.Name(),
		ScreenID:  nativeSettingsScreenDashboard,
		State:     raw,
		TTL:       nativeSettingsTTL,
		View:      view,
	})
	return err
}

func (p *Plugin) handleNativeSettingsSlot(ctx *orchestration.Context, slot int) error {
	if p == nil || ctx == nil {
		return orchestration.ErrInvalidEngine
	}
	state, err := decodeNativeSettingsState(ctx.State())
	if err != nil || slot < 0 || slot >= len(state.Slots) {
		return ctx.Answer("Settings interaction expired. Reopen .settings.", true)
	}
	intent := state.Slots[slot]
	menu := state.Menu
	menu.OwnerID = ctx.Session().Binding.ActorID
	if menu.ChatID == 0 {
		menu.ChatID = ctx.Session().Binding.ChatID
	}

	switch intent.Kind {
	case nativeIntentNoop:
		if err := ctx.Touch(nativeSettingsTTL); err != nil {
			return err
		}
		return ctx.Answer("", false)

	case nativeIntentClose:
		if err := ctx.Terminate(presentation.View{Text: "✅ Settings dashboard closed."}); err != nil {
			return err
		}
		return ctx.Answer("", false)

	case nativeIntentHome:
		menu.Category = ""
		menu.Page = 1
		menu.Target = nil
		menu.Selected = ""
		menu.ActionValue = ""

	case nativeIntentCategory:
		menu.Category = intent.Category
		menu.Page = 1
		menu.Target = nil
		menu.Selected = ""
		menu.ActionValue = ""

	case nativeIntentTarget:
		menu.SetTarget(intent.Namespace, intent.Key)
		menu.ActionValue = ""

	case nativeIntentPage:
		menu.Page = intent.Page
		menu.Target = nil
		menu.Selected = ""
		menu.ActionValue = ""

	case nativeIntentScope:
		menu.Scope = intent.Scope
		menu.ScopeID = intent.ScopeID
		menu.Target = nil
		menu.Selected = ""
		menu.ActionValue = ""

	case nativeIntentBack:
		menu.Target = nil
		menu.Selected = ""
		menu.ActionValue = ""

	case nativeIntentToggle, nativeIntentSet, nativeIntentReset:
		menu.SetTarget(intent.Namespace, intent.Key)
		menu.ActionValue = intent.Value
		action := intent.Kind
		if action == nativeIntentSet {
			action = "set"
		}
		if err := p.applySettingMutation(ctx.Context(), ctx.Session().Binding.ActorID, &menu, action); err != nil {
			return ctx.Answer(ui.MapUserErrorMessage(err), true)
		}
		menu.ActionValue = ""

	default:
		return ctx.Answer("Settings interaction is invalid. Reopen .settings.", true)
	}

	raw, view, err := p.nativeSettingsView(ctx.Context(), menu)
	if err != nil {
		return err
	}
	return ctx.Transition(raw, nativeSettingsTTL, view)
}

func decodeNativeSettingsState(raw []byte) (nativeSettingsState, error) {
	if len(raw) == 0 {
		return nativeSettingsState{}, fmt.Errorf("settings: empty native state")
	}
	var state nativeSettingsState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nativeSettingsState{}, fmt.Errorf("settings: decode native state: %w", err)
	}
	if state.Menu.Page <= 0 {
		state.Menu.Page = 1
	}
	return state, nil
}

func encodeNativeSettingsState(state nativeSettingsState) ([]byte, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("settings: encode native state: %w", err)
	}
	return raw, nil
}

type nativeSettingsViewBuilder struct {
	state nativeSettingsState
	rows  []presentation.Row
}

func (b *nativeSettingsViewBuilder) add(text string, intent nativeSettingsIntent) (presentation.Button, error) {
	if len(b.state.Slots) >= nativeSettingsActionSlotCount {
		return presentation.Button{}, fmt.Errorf("settings: native screen exceeds %d action slots", nativeSettingsActionSlotCount)
	}
	slot := len(b.state.Slots)
	b.state.Slots = append(b.state.Slots, intent)
	return presentation.Button{Text: text, ActionID: nativeSettingsSlotID(slot)}, nil
}

func (b *nativeSettingsViewBuilder) addRow(buttons ...presentation.Button) {
	if len(buttons) > 0 {
		b.rows = append(b.rows, presentation.Row(buttons))
	}
}

func (p *Plugin) nativeSettingsView(ctx context.Context, menu MenuState) ([]byte, presentation.View, error) {
	if p == nil || p.service == nil || p.service.Registry() == nil {
		return nil, presentation.View{}, fmt.Errorf("settings: service unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if menu.Page <= 0 {
		menu.Page = 1
	}
	builder := &nativeSettingsViewBuilder{state: nativeSettingsState{Menu: menu}}

	var text string
	ns, key := menu.GetTarget()
	switch {
	case ns != "" && key != "":
		var err error
		text, err = p.buildNativeDetail(ctx, builder)
		if err != nil {
			return nil, presentation.View{}, err
		}
	case menu.Category != "":
		var err error
		text, err = p.buildNativeCategory(ctx, builder)
		if err != nil {
			return nil, presentation.View{}, err
		}
	default:
		var err error
		text, err = p.buildNativeHome(ctx, builder)
		if err != nil {
			return nil, presentation.View{}, err
		}
	}

	raw, err := encodeNativeSettingsState(builder.state)
	if err != nil {
		return nil, presentation.View{}, err
	}
	view := presentation.View{Text: text, Rows: builder.rows}
	if err := view.Validate(); err != nil {
		return nil, presentation.View{}, err
	}
	return raw, view, nil
}

func (p *Plugin) buildNativeHome(_ context.Context, builder *nativeSettingsViewBuilder) (string, error) {
	menu := builder.state.Menu
	screen := ui.NewScreen(
		"settings:home",
		"⚙️ GoUltroid Settings Dashboard",
		"Welcome to the interactive configuration dashboard.\nSelect a category below to view and modify settings:\n",
	)
	categories := p.service.Registry().Categories()
	row := make([]presentation.Button, 0, 2)
	for _, category := range categories {
		button, err := builder.add(settingsCategoryLabel(category), nativeSettingsIntent{
			Kind:     nativeIntentCategory,
			Category: category,
		})
		if err != nil {
			return "", err
		}
		row = append(row, button)
		if len(row) == 2 {
			builder.addRow(row...)
			row = row[:0]
		}
	}
	if len(row) > 0 {
		builder.addRow(row...)
	}

	nextScope, nextScopeID, scopeText := p.nextScope(menu)
	scopeButton, err := builder.add(scopeText, nativeSettingsIntent{
		Kind:    nativeIntentScope,
		Scope:   nextScope,
		ScopeID: nextScopeID,
	})
	if err != nil {
		return "", err
	}
	builder.addRow(scopeButton)

	closeButton, err := builder.add("❌ Close", nativeSettingsIntent{Kind: nativeIntentClose})
	if err != nil {
		return "", err
	}
	builder.addRow(closeButton)
	return screen.Text(), nil
}

func (p *Plugin) buildNativeCategory(ctx context.Context, builder *nativeSettingsViewBuilder) (string, error) {
	menu := builder.state.Menu
	defs := p.service.Registry().ListByCategory(menu.Category)
	title := fmt.Sprintf("⚙️ Settings: %s", strings.Title(menu.Category))
	pagedDefs, totalPages := ui.PaginateSlice(defs, menu.Page, nativeSettingsPageSize)
	if menu.Page < 1 {
		menu.Page = 1
	} else if menu.Page > totalPages {
		menu.Page = totalPages
	}
	builder.state.Menu = menu
	if len(defs) == 0 {
		screen := ui.NewScreen("settings:cat", title, "No settings configured for this category.")
		homeButton, err := builder.add("🏠 Home", nativeSettingsIntent{Kind: nativeIntentHome})
		if err != nil {
			return "", err
		}
		closeButton, err := builder.add("❌ Close", nativeSettingsIntent{Kind: nativeIntentClose})
		if err != nil {
			return "", err
		}
		builder.addRow(homeButton, closeButton)
		return screen.Text(), nil
	}

	var body strings.Builder
	body.WriteString(fmt.Sprintf(
		"Category: <b>%s</b> | Scope: <b>%s</b>\n\n",
		ui.EscapeHTML(strings.Title(menu.Category)),
		ui.EscapeHTML(string(menu.Scope)),
	))
	for _, def := range pagedDefs {
		currentVal, _ := p.service.Resolve(ctx, menu.OwnerID, menu.ScopeID, def.Namespace, def.Key)
		body.WriteString(fmt.Sprintf(
			"• <b>%s</b> (<code>%s:%s</code>)\n  Val: <code>%s</code> | <i>%s</i>\n",
			ui.EscapeHTML(def.Title),
			ui.EscapeHTML(def.Namespace),
			ui.EscapeHTML(def.Key),
			ui.EscapeHTML(currentVal),
			ui.EscapeHTML(def.Description),
		))

		var (
			label  string
			intent nativeSettingsIntent
		)
		if def.Type == settingssvc.TypeBool {
			if strings.EqualFold(currentVal, "true") {
				label = "✅ " + def.Title
			} else {
				label = "❌ " + def.Title
			}
			intent = nativeSettingsIntent{
				Kind:      nativeIntentToggle,
				Namespace: def.Namespace,
				Key:       def.Key,
			}
		} else {
			label = "⚙️ Edit " + def.Title
			intent = nativeSettingsIntent{
				Kind:      nativeIntentTarget,
				Namespace: def.Namespace,
				Key:       def.Key,
			}
		}
		button, err := builder.add(label, intent)
		if err != nil {
			return "", err
		}
		builder.addRow(button)
	}
	if totalPages > 1 {
		pagination := make(presentation.Row, 0, 3)
		if menu.Page > 1 {
			prev, err := builder.add("◀ Prev", nativeSettingsIntent{Kind: nativeIntentPage, Page: menu.Page - 1})
			if err != nil {
				return "", err
			}
			pagination = append(pagination, prev)
		} else {
			left, err := builder.add("◀", nativeSettingsIntent{Kind: nativeIntentNoop})
			if err != nil {
				return "", err
			}
			pagination = append(pagination, left)
		}
		counter, err := builder.add(fmt.Sprintf("%d / %d", menu.Page, totalPages), nativeSettingsIntent{Kind: nativeIntentNoop})
		if err != nil {
			return "", err
		}
		pagination = append(pagination, counter)
		if menu.Page < totalPages {
			next, err := builder.add("Next ▶", nativeSettingsIntent{Kind: nativeIntentPage, Page: menu.Page + 1})
			if err != nil {
				return "", err
			}
			pagination = append(pagination, next)
		} else {
			right, err := builder.add("▶", nativeSettingsIntent{Kind: nativeIntentNoop})
			if err != nil {
				return "", err
			}
			pagination = append(pagination, right)
		}
		builder.addRow(pagination...)
	}

	homeButton, err := builder.add("🏠 Home", nativeSettingsIntent{Kind: nativeIntentHome})
	if err != nil {
		return "", err
	}
	closeButton, err := builder.add("❌ Close", nativeSettingsIntent{Kind: nativeIntentClose})
	if err != nil {
		return "", err
	}
	builder.addRow(homeButton, closeButton)
	return ui.NewScreen("settings:cat", title, body.String()).Text(), nil
}

func (p *Plugin) buildNativeDetail(ctx context.Context, builder *nativeSettingsViewBuilder) (string, error) {
	menu := builder.state.Menu
	ns, key := menu.GetTarget()
	def, ok := p.service.Registry().Get(ns, key)
	if !ok {
		menu.Target = nil
		menu.Selected = ""
		builder.state.Menu = menu
		return p.buildNativeCategory(ctx, builder)
	}
	currentVal, err := p.service.Resolve(ctx, menu.OwnerID, menu.ScopeID, ns, key)
	if err != nil {
		return "", err
	}
	originBadge := p.settingOriginBadge(ctx, menu, ns, key)
	body := fmt.Sprintf(
		"<b>%s</b>\n%s\n\n<b>Type:</b> <code>%s</code>\n<b>Current Value:</b> <code>%s</code>\n<b>Origin:</b> %s\n<b>Default:</b> <code>%s</code>\n<b>Scope:</b> <code>%s</code>\n",
		ui.EscapeHTML(def.Title),
		ui.EscapeHTML(def.Description),
		def.Type,
		ui.EscapeHTML(currentVal),
		originBadge,
		ui.EscapeHTML(def.DefaultValue),
		menu.Scope,
	)
	screen := ui.NewScreen("settings:detail", fmt.Sprintf("⚙️ %s (%s:%s)", def.Title, ns, key), body)

	switch def.Type {
	case settingssvc.TypeBool:
		label := "🔴 " + def.Title + ": OFF"
		if strings.EqualFold(currentVal, "true") {
			label = "🟢 " + def.Title + ": ON"
		}
		button, err := builder.add(label, nativeSettingsIntent{
			Kind:      nativeIntentToggle,
			Namespace: ns,
			Key:       key,
		})
		if err != nil {
			return "", err
		}
		builder.addRow(button)

	case settingssvc.TypeInt:
		current, _ := strconv.ParseInt(currentVal, 10, 64)
		row := make(presentation.Row, 0, 3)
		if def.MinVal != nil && current <= *def.MinVal {
			button, err := builder.add("⏹", nativeSettingsIntent{Kind: nativeIntentNoop})
			if err != nil {
				return "", err
			}
			row = append(row, button)
		} else {
			button, err := builder.add("➖", nativeSettingsIntent{
				Kind:      nativeIntentSet,
				Namespace: ns,
				Key:       key,
				Value:     strconv.FormatInt(current-1, 10),
			})
			if err != nil {
				return "", err
			}
			row = append(row, button)
		}
		valueButton, err := builder.add(strconv.FormatInt(current, 10), nativeSettingsIntent{Kind: nativeIntentNoop})
		if err != nil {
			return "", err
		}
		row = append(row, valueButton)
		if def.MaxVal != nil && current >= *def.MaxVal {
			button, err := builder.add("⏹", nativeSettingsIntent{Kind: nativeIntentNoop})
			if err != nil {
				return "", err
			}
			row = append(row, button)
		} else {
			button, err := builder.add("➕", nativeSettingsIntent{
				Kind:      nativeIntentSet,
				Namespace: ns,
				Key:       key,
				Value:     strconv.FormatInt(current+1, 10),
			})
			if err != nil {
				return "", err
			}
			row = append(row, button)
		}
		builder.addRow(row...)

	case settingssvc.TypeDuration:
		selected, _ := time.ParseDuration(currentVal)
		row := make(presentation.Row, 0, 3)
		for _, duration := range nativeDurationPresets {
			label := duration.String()
			if duration == selected {
				label += " ●"
			}
			button, err := builder.add(label, nativeSettingsIntent{
				Kind:      nativeIntentSet,
				Namespace: ns,
				Key:       key,
				Value:     duration.String(),
			})
			if err != nil {
				return "", err
			}
			row = append(row, button)
			if len(row) == 3 {
				builder.addRow(row...)
				row = row[:0]
			}
		}
		if len(row) > 0 {
			builder.addRow(row...)
		}

	case settingssvc.TypeEnum:
		row := make(presentation.Row, 0, 3)
		for _, option := range def.AllowedValues {
			label := "○ " + option
			if strings.EqualFold(option, currentVal) {
				label = "● " + option
			}
			button, err := builder.add(label, nativeSettingsIntent{
				Kind:      nativeIntentSet,
				Namespace: ns,
				Key:       key,
				Value:     option,
			})
			if err != nil {
				return "", err
			}
			row = append(row, button)
			if len(row) == 3 {
				builder.addRow(row...)
				row = row[:0]
			}
		}
		if len(row) > 0 {
			builder.addRow(row...)
		}
	}

	resetLabel := "🔄 Reset to Default"
	if originBadge != "⚙️ Schema Default" && originBadge != "🌐 Global Setting" {
		resetLabel = "↩ Reset Override"
	}
	resetButton, err := builder.add(resetLabel, nativeSettingsIntent{
		Kind:      nativeIntentReset,
		Namespace: ns,
		Key:       key,
	})
	if err != nil {
		return "", err
	}
	backButton, err := builder.add("🔙 Back to "+strings.Title(def.Category), nativeSettingsIntent{Kind: nativeIntentBack})
	if err != nil {
		return "", err
	}
	builder.addRow(resetButton)
	builder.addRow(backButton)
	return screen.Text(), nil
}

var _ nativeinteraction.FeatureDriver = (*Plugin)(nil)
var _ interface {
	FeatureSpec() feature.Spec
} = (*Plugin)(nil)
