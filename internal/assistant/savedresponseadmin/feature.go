package savedresponseadmin

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

const (
	FeatureID = "savedresponse_admin"
	CommandID = "responses"

	screenHome   = "home"
	screenList   = "list"
	screenDetail = "detail"
	screenInput  = "input"

	actionHome             = "home"
	actionSurfaceAssistant = "surface_assistant"
	actionSurfaceInline    = "surface_inline"
	actionSurfaceDeepLink  = "surface_deep_link"
	actionSurfaceCallback  = "surface_callback"
	actionPrev             = "prev"
	actionNext             = "next"
	actionCreate           = "create"
	actionBack             = "back"
	actionToggle           = "toggle"
	actionEdit             = "edit"
	actionDelete           = "delete"
	actionDeleteConfirm    = "delete_confirm"
	actionDeleteCancel     = "delete_cancel"

	pageSize  = 6
	slotCount = 6

	sessionTTL = 10 * time.Minute
	inputTTL   = 3 * time.Minute
)

var ErrUnavailable = errors.New("saved-response admin: runtime unavailable")

type state struct {
	Surface     savedresponse.Surface
	Page        int
	Slots       []string
	Selected    string
	Revision    uint64
	Incarnation string
	Wizard      string
}

type Feature struct {
	bindings *savedresponse.BindingService

	mu sync.RWMutex
	rt assistantinteraction.DriverRuntime
}

func New(bindings *savedresponse.BindingService) *Feature {
	return &Feature{bindings: bindings}
}

func (*Feature) Name() string { return FeatureID }
func (*Feature) Init() error  { return nil }

func (f *Feature) Commands() []core.Command {
	return []core.Command{{
		Name:        CommandID,
		Aliases:     []string{"responsebindings"},
		Description: "Manage persistent SavedResponse surface bindings",
		Usage:       "/responses",
		Category:    "Assistant",
		Permission: core.PermissionOwner,
		Invocation: core.InvocationPolicy{
			Assistant: core.InvocationSelfOnly,
		},
		Surfaces:    execution.SurfaceAssistant,
		PrivateOnly: true,
		Handler:     f.openCommand,
	}}
}

func (*Feature) AssistantFeatureID() string { return FeatureID }

func (*Feature) FeatureSpec() feature.Spec {
	surface := execution.SurfaceAssistant
	policy := feature.OwnerPolicy(surface)
	policy.PrivateOnly = true
	interactions := []feature.Interaction{
		{ID: screenHome, Kind: feature.InteractionScreen, Description: "SavedResponse binding control home", Surfaces: surface, Policy: policy},
		{ID: screenList, Kind: feature.InteractionScreen, Description: "SavedResponse binding list", Surfaces: surface, Policy: policy},
		{ID: screenDetail, Kind: feature.InteractionScreen, Description: "SavedResponse binding detail", Surfaces: surface, Policy: policy},
		{ID: screenInput, Kind: feature.InteractionScreen, Description: "SavedResponse binding input", Surfaces: surface, Policy: policy},
	}
	for _, id := range []string{
		actionHome, actionSurfaceAssistant, actionSurfaceInline, actionSurfaceDeepLink, actionSurfaceCallback,
		actionPrev, actionNext, actionCreate, actionBack, actionToggle, actionEdit, actionDelete,
		actionDeleteConfirm, actionDeleteCancel,
	} {
		interactions = append(interactions, feature.Interaction{
			ID: id, Kind: feature.InteractionAction, Description: "SavedResponse binding management action",
			Surfaces: surface, Policy: policy,
		})
	}
	for i := 0; i < slotCount; i++ {
		interactions = append(interactions, feature.Interaction{
			ID: slotID(i), Kind: feature.InteractionAction, Description: "SavedResponse binding selection slot",
			Surfaces: surface, Policy: policy,
		})
	}
	return feature.Spec{
		ID: FeatureID, Name: "SavedResponse Bindings",
		Description: "Owner-only a2 control plane for persistent SavedResponse surface bindings.",
		Category: "Assistant", Interactions: interactions,
	}
}

func slotID(index int) string { return fmt.Sprintf("slot_%02d", index) }

func (f *Feature) BindAssistant(rt assistantinteraction.DriverRuntime) (func(), error) {
	if f == nil || f.bindings == nil || rt.Engine == nil || rt.Catalog == nil || rt.Admit == nil {
		return nil, ErrUnavailable
	}
	scope, ok := rt.Catalog.FeatureScope(FeatureID)
	if !ok || scope.IsZero() {
		return nil, ErrUnavailable
	}

	type closer interface{ Close() }
	var registrations []closer
	register := func(actionID string, handler func(*orchestration.Context) error) error {
		reg, err := rt.Engine.RegisterAction(scope, FeatureID, actionID, func(ctx *orchestration.Context) error {
			if ctx == nil {
				return ErrUnavailable
			}
			session := ctx.Session()
			if err := rt.Admit(FeatureID, feature.InteractionAction, actionID, session.Binding.ActorID, ctx.Target()); err != nil {
				return err
			}
			return handler(ctx)
		})
		if err != nil {
			return err
		}
		registrations = append(registrations, reg)
		return nil
	}

	handlers := map[string]func(*orchestration.Context) error{
		actionHome:             f.handleHome,
		actionSurfaceAssistant: func(ctx *orchestration.Context) error { return f.openSurface(ctx, savedresponse.SurfaceAssistantCommand) },
		actionSurfaceInline:    func(ctx *orchestration.Context) error { return f.openSurface(ctx, savedresponse.SurfaceInline) },
		actionSurfaceDeepLink:  func(ctx *orchestration.Context) error { return f.openSurface(ctx, savedresponse.SurfaceDeepLink) },
		actionSurfaceCallback:  func(ctx *orchestration.Context) error { return f.openSurface(ctx, savedresponse.SurfaceCallback) },
		actionPrev:             func(ctx *orchestration.Context) error { return f.changePage(ctx, -1) },
		actionNext:             func(ctx *orchestration.Context) error { return f.changePage(ctx, 1) },
		actionCreate:           f.beginCreate,
		actionBack:             f.backToList,
		actionToggle:           f.toggleSelected,
		actionEdit:             f.beginEdit,
		actionDelete:           f.confirmDelete,
		actionDeleteConfirm:    f.deleteSelected,
		actionDeleteCancel:     f.showSelected,
	}
	for _, id := range []string{
		actionHome, actionSurfaceAssistant, actionSurfaceInline, actionSurfaceDeepLink, actionSurfaceCallback,
		actionPrev, actionNext, actionCreate, actionBack, actionToggle, actionEdit, actionDelete,
		actionDeleteConfirm, actionDeleteCancel,
	} {
		if err := register(id, handlers[id]); err != nil {
			for i := len(registrations) - 1; i >= 0; i-- {
				registrations[i].Close()
			}
			return nil, err
		}
	}
	for i := 0; i < slotCount; i++ {
		slot := i
		if err := register(slotID(slot), func(ctx *orchestration.Context) error {
			return f.selectSlot(ctx, slot)
		}); err != nil {
			for i := len(registrations) - 1; i >= 0; i-- {
				registrations[i].Close()
			}
			return nil, err
		}
	}

	f.mu.Lock()
	f.rt = rt
	f.mu.Unlock()
	return func() {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
		f.mu.Lock()
		if f.rt.Engine == rt.Engine {
			f.rt = assistantinteraction.DriverRuntime{}
		}
		f.mu.Unlock()
	}, nil
}

func (f *Feature) runtime() assistantinteraction.DriverRuntime {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.rt
}

func (f *Feature) openCommand(ctx *core.Context) error {
	if f == nil || ctx == nil || ctx.PeerID == nil || ctx.SenderID() == 0 {
		return ErrUnavailable
	}
	rt := f.runtime()
	if rt.Engine == nil || rt.Admit == nil {
		return ErrUnavailable
	}
	target := presentationtelegram.MessageTarget{Peer: ctx.PeerID, ChatID: ctx.ChatID()}
	if err := rt.Admit(FeatureID, feature.InteractionScreen, screenHome, ctx.SenderID(), target); err != nil {
		return err
	}
	_, err := rt.Engine.Begin(ctx.Ctx, orchestration.BeginRequest{
		FeatureID: FeatureID,
		ActorID:   ctx.SenderID(),
		State:     encodeState(state{}),
		TTL:       sessionTTL,
		Target:    target,
		View:      homeView(),
	})
	return err
}

func homeView() presentation.View {
	return presentation.View{
		Text: "🗂️ <b>SavedResponse Bindings</b>\n\nKelola routing persistent tanpa menyalin payload response. Alias yang sama boleh dipakai di surface berbeda; collision dengan handler canonical pada surface yang sama ditolak.",
		Rows: []presentation.Row{
			{{Text: "🤖 Assistant commands", ActionID: actionSurfaceAssistant}},
			{{Text: "🔎 Inline", ActionID: actionSurfaceInline}, {Text: "🔗 Deep links", ActionID: actionSurfaceDeepLink}},
			{{Text: "🔘 Callbacks", ActionID: actionSurfaceCallback}},
		},
	}
}

func decodeState(raw []byte) state {
	var s state
	_ = json.Unmarshal(raw, &s)
	return s
}

func encodeState(s state) []byte {
	raw, _ := json.Marshal(s)
	return raw
}

func surfaceLabel(surface savedresponse.Surface) string {
	switch surface {
	case savedresponse.SurfaceAssistantCommand:
		return "Assistant command"
	case savedresponse.SurfaceInline:
		return "Inline"
	case savedresponse.SurfaceDeepLink:
		return "Deep link"
	case savedresponse.SurfaceCallback:
		return "Callback"
	default:
		return string(surface)
	}
}

func (f *Feature) handleHome(ctx *orchestration.Context) error {
	return ctx.Transition(encodeState(state{}), sessionTTL, homeView())
}

func (f *Feature) openSurface(ctx *orchestration.Context, surface savedresponse.Surface) error {
	return f.renderList(ctx, state{Surface: surface})
}

func (f *Feature) renderList(ctx *orchestration.Context, s state) error {
	if s.Surface == "" {
		return f.handleHome(ctx)
	}
	items, err := f.bindings.List(ctx.Context(), s.Surface, true, savedresponse.MaxBindingList)
	if err != nil {
		return err
	}
	maxPage := 0
	if len(items) > 0 {
		maxPage = (len(items) - 1) / pageSize
	}
	if s.Page < 0 {
		s.Page = 0
	}
	if s.Page > maxPage {
		s.Page = maxPage
	}
	start := s.Page * pageSize
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	s.Slots = s.Slots[:0]
	s.Selected = ""
	s.Revision = 0
	s.Incarnation = ""
	s.Wizard = ""

	rows := make([]presentation.Row, 0, pageSize+2)
	var body strings.Builder
	fmt.Fprintf(&body, "🗂️ <b>%s bindings</b>\n\n", html.EscapeString(surfaceLabel(s.Surface)))
	if len(items) == 0 {
		body.WriteString("<i>Belum ada binding.</i>\n")
	}
	for i := start; i < end; i++ {
		item := items[i]
		status := "✅"
		if !item.Enabled {
			status = "⏸"
		}
		s.Slots = append(s.Slots, item.Alias)
		rows = append(rows, presentation.Row{{
			Text:     fmt.Sprintf("%s %s", status, item.Alias),
			ActionID: slotID(i - start),
		}})
		fmt.Fprintf(&body, "%s <code>%s</code> → <code>%s:%d:%s</code>\n",
			status,
			html.EscapeString(item.Alias),
			html.EscapeString(item.Reference.Provider),
			item.Reference.ScopeID,
			html.EscapeString(item.Reference.Key),
		)
	}
	nav := presentation.Row{}
	if s.Page > 0 {
		nav = append(nav, presentation.Button{Text: "⬅️ Prev", ActionID: actionPrev})
	}
	if s.Page < maxPage {
		nav = append(nav, presentation.Button{Text: "Next ➡️", ActionID: actionNext})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows,
		presentation.Row{{Text: "➕ Create", ActionID: actionCreate}, {Text: "🏠 Home", ActionID: actionHome}},
	)
	return ctx.Transition(encodeState(s), sessionTTL, presentation.View{Text: body.String(), Rows: rows})
}

func (f *Feature) changePage(ctx *orchestration.Context, delta int) error {
	s := decodeState(ctx.State())
	s.Page += delta
	return f.renderList(ctx, s)
}

func (f *Feature) selectSlot(ctx *orchestration.Context, slot int) error {
	s := decodeState(ctx.State())
	if slot < 0 || slot >= len(s.Slots) {
		return ctx.Answer("Binding list sudah berubah. Refresh surface.", true)
	}
	s.Selected = s.Slots[slot]
	current, err := f.bindings.Get(ctx.Context(), s.Surface, s.Selected)
	if err != nil {
		return err
	}
	if current == nil {
		return ctx.Answer("Binding sudah dihapus. Kembali ke list.", true)
	}
	s.Revision = current.Revision
	s.Incarnation = current.Incarnation
	return f.renderDetail(ctx, s, *current)
}

func (f *Feature) showSelected(ctx *orchestration.Context) error {
	s := decodeState(ctx.State())
	if s.Surface == "" || s.Selected == "" {
		return f.renderList(ctx, s)
	}
	current, err := f.bindings.Get(ctx.Context(), s.Surface, s.Selected)
	if err != nil {
		return err
	}
	if current == nil {
		return f.renderList(ctx, s)
	}
	s.Revision = current.Revision
	s.Incarnation = current.Incarnation
	return f.renderDetail(ctx, s, *current)
}

func (f *Feature) renderDetail(ctx *orchestration.Context, s state, binding savedresponse.SurfaceBinding) error {
	s.Selected = binding.Alias
	s.Revision = binding.Revision
	s.Incarnation = binding.Incarnation
	s.Wizard = ""
	status := "enabled"
	toggleText := "⏸ Disable"
	if !binding.Enabled {
		status = "disabled"
		toggleText = "▶️ Enable"
	}
	text := fmt.Sprintf(
		"🔎 <b>SavedResponse Binding</b>\n\nSurface: <code>%s</code>\nAlias: <code>%s</code>\nStatus: <b>%s</b>\nProvider: <code>%s</code>\nScope ID: <code>%d</code>\nKey: <code>%s</code>\nRevision: <code>%d</code>",
		html.EscapeString(string(binding.Surface)),
		html.EscapeString(binding.Alias),
		status,
		html.EscapeString(binding.Reference.Provider),
		binding.Reference.ScopeID,
		html.EscapeString(binding.Reference.Key),
		binding.Revision,
	)
	return ctx.Transition(encodeState(s), sessionTTL, presentation.View{
		Text: text,
		Rows: []presentation.Row{
			{{Text: toggleText, ActionID: actionToggle}, {Text: "✏️ Edit ref", ActionID: actionEdit}},
			{{Text: "🗑 Delete", ActionID: actionDelete}, {Text: "⬅️ List", ActionID: actionBack}},
		},
	})
}

func (f *Feature) backToList(ctx *orchestration.Context) error {
	return f.renderList(ctx, decodeState(ctx.State()))
}

func (f *Feature) beginCreate(ctx *orchestration.Context) error {
	s := decodeState(ctx.State())
	if s.Surface == "" {
		return f.handleHome(ctx)
	}
	s.Wizard = "create"
	s.Selected = ""
	s.Revision = 0
	s.Incarnation = ""
	return ctx.AwaitInput(
		encodeState(s),
		inputTTL,
		presentation.View{
			Text: fmt.Sprintf(
				"➕ <b>Create %s binding</b>\n\nKirim satu baris:\n<code>&lt;alias&gt; &lt;provider&gt; &lt;scope_id&gt; &lt;key&gt;</code>\n\nContoh: <code>welcome notes 42 welcome</code>\n\nKetik <code>/cancel</code> untuk batal.",
				html.EscapeString(surfaceLabel(s.Surface)),
			),
			Rows: []presentation.Row{{{Text: "❌ Cancel", ActionID: actionBack}}},
		},
	)
}

func (f *Feature) beginEdit(ctx *orchestration.Context) error {
	s := decodeState(ctx.State())
	if s.Selected == "" {
		return f.renderList(ctx, s)
	}
	s.Wizard = "edit"
	return ctx.AwaitInput(
		encodeState(s),
		inputTTL,
		presentation.View{
			Text: fmt.Sprintf(
				"✏️ <b>Edit reference</b> <code>%s</code>\n\nKirim satu baris:\n<code>&lt;provider&gt; &lt;scope_id&gt; &lt;key&gt;</code>\n\nAlias dan surface tidak berubah. Ketik <code>/cancel</code> untuk batal.",
				html.EscapeString(s.Selected),
			),
			Rows: []presentation.Row{{{Text: "❌ Cancel", ActionID: actionDeleteCancel}}},
		},
	)
}

func (f *Feature) toggleSelected(ctx *orchestration.Context) error {
	s := decodeState(ctx.State())
	current, err := f.bindings.Get(ctx.Context(), s.Surface, s.Selected)
	if err != nil {
		return err
	}
	if current == nil {
		return f.renderList(ctx, s)
	}
	if current.Revision != s.Revision || current.Incarnation != s.Incarnation {
		_ = ctx.Answer("Binding berubah sejak screen dirender; data direfresh.", true)
		s.Revision = current.Revision
		s.Incarnation = current.Incarnation
		return f.renderDetail(ctx, s, *current)
	}
	updated, err := f.bindings.SetEnabled(
		ctx.Context(), current.Surface, current.Alias, !current.Enabled,
		current.Revision, current.Incarnation,
	)
	if err != nil {
		if errors.Is(err, savedresponse.ErrBindingReserved) {
			return ctx.Answer("Alias ini sekarang reserved oleh handler canonical pada surface tersebut.", true)
		}
		return err
	}
	_ = ctx.Answer("Binding diperbarui.", false)
	return f.renderDetail(ctx, s, updated)
}

func (f *Feature) confirmDelete(ctx *orchestration.Context) error {
	s := decodeState(ctx.State())
	if s.Selected == "" {
		return f.renderList(ctx, s)
	}
	return ctx.Transition(
		encodeState(s),
		sessionTTL,
		presentation.View{
			Text: fmt.Sprintf("⚠️ Hapus binding <code>%s</code> dari surface <code>%s</code>?\n\nResponse provider tidak ikut dihapus.",
				html.EscapeString(s.Selected), html.EscapeString(string(s.Surface))),
			Rows: []presentation.Row{{
				{Text: "🗑 Confirm", ActionID: actionDeleteConfirm},
				{Text: "Cancel", ActionID: actionDeleteCancel},
			}},
		},
	)
}

func (f *Feature) deleteSelected(ctx *orchestration.Context) error {
	s := decodeState(ctx.State())
	if err := f.bindings.Delete(
		ctx.Context(), s.Surface, s.Selected, s.Revision, s.Incarnation,
	); err != nil {
		if errors.Is(err, savedresponse.ErrBindingConflict) {
			current, getErr := f.bindings.Get(ctx.Context(), s.Surface, s.Selected)
			if getErr != nil {
				return getErr
			}
			if current == nil {
				return f.renderList(ctx, s)
			}
			_ = ctx.Answer("Binding berubah; detail direfresh.", true)
			return f.renderDetail(ctx, s, *current)
		}
		if errors.Is(err, savedresponse.ErrBindingNotFound) {
			return f.renderList(ctx, s)
		}
		return err
	}
	_ = ctx.Answer("Binding dihapus.", false)
	s.Selected = ""
	s.Revision = 0
	s.Incarnation = ""
	return f.renderList(ctx, s)
}

func (f *Feature) HandleAssistantInput(ctx *orchestration.Context, input string) error {
	if f == nil || ctx == nil {
		return ErrUnavailable
	}
	rt := f.runtime()
	session := ctx.Session()
	if rt.Admit == nil {
		return ErrUnavailable
	}
	if err := rt.Admit(FeatureID, feature.InteractionScreen, screenInput, session.Binding.ActorID, ctx.Target()); err != nil {
		return err
	}
	s := decodeState(ctx.State())
	input = strings.TrimSpace(input)
	if strings.EqualFold(input, "/cancel") {
		if s.Selected != "" {
			return f.showSelected(ctx)
		}
		return f.renderList(ctx, s)
	}
	switch s.Wizard {
	case "create":
		return f.applyCreate(ctx, s, input)
	case "edit":
		return f.applyEdit(ctx, s, input)
	default:
		return ErrUnavailable
	}
}

func parseReference(fields []string) (savedresponse.Reference, error) {
	if len(fields) < 3 {
		return savedresponse.Reference{}, fmt.Errorf("expected provider scope_id key")
	}
	scopeID, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return savedresponse.Reference{}, fmt.Errorf("scope_id must be int64")
	}
	ref := savedresponse.Reference{Provider: fields[0], ScopeID: scopeID, Key: strings.Join(fields[2:], " ")}
	if err := ref.Validate(); err != nil {
		return savedresponse.Reference{}, err
	}
	return ref, nil
}

func (f *Feature) rearm(ctx *orchestration.Context, s state, message string) error {
	cancelAction := actionBack
	if s.Selected != "" {
		cancelAction = actionDeleteCancel
	}
	return ctx.AwaitInput(
		encodeState(s),
		inputTTL,
		presentation.View{
			Text: "⚠️ " + html.EscapeString(message) + "\n\nPerbaiki input atau ketik <code>/cancel</code>.",
			Rows: []presentation.Row{{{Text: "❌ Cancel", ActionID: cancelAction}}},
		},
	)
}

func (f *Feature) applyCreate(ctx *orchestration.Context, s state, input string) error {
	fields := strings.Fields(input)
	if len(fields) < 4 {
		return f.rearm(ctx, s, "format: <alias> <provider> <scope_id> <key>")
	}
	ref, err := parseReference(fields[1:])
	if err != nil {
		return f.rearm(ctx, s, err.Error())
	}
	created, err := f.bindings.Create(ctx.Context(), savedresponse.SurfaceBinding{
		Surface: s.Surface, Alias: fields[0], Reference: ref, Enabled: true,
	})
	if err != nil {
		switch {
		case errors.Is(err, savedresponse.ErrBindingReserved):
			return f.rearm(ctx, s, "alias reserved oleh handler canonical pada surface ini")
		case errors.Is(err, savedresponse.ErrBindingExists):
			return f.rearm(ctx, s, "alias sudah ada pada surface ini")
		default:
			return f.rearm(ctx, s, err.Error())
		}
	}
	s.Selected = created.Alias
	s.Revision = created.Revision
	s.Incarnation = created.Incarnation
	s.Wizard = ""
	return f.renderDetail(ctx, s, created)
}

func (f *Feature) applyEdit(ctx *orchestration.Context, s state, input string) error {
	ref, err := parseReference(strings.Fields(input))
	if err != nil {
		return f.rearm(ctx, s, err.Error())
	}
	current, err := f.bindings.Get(ctx.Context(), s.Surface, s.Selected)
	if err != nil {
		return err
	}
	if current == nil {
		return f.renderList(ctx, s)
	}
	if current.Revision != s.Revision || current.Incarnation != s.Incarnation {
		s.Wizard = ""
		return f.renderDetail(ctx, s, *current)
	}
	updated, err := f.bindings.Update(
		ctx.Context(), current.Surface, current.Alias, ref, current.Enabled,
		current.Revision, current.Incarnation,
	)
	if err != nil {
		switch {
		case errors.Is(err, savedresponse.ErrBindingReserved):
			return f.rearm(ctx, s, "alias sekarang reserved oleh handler canonical")
		case errors.Is(err, savedresponse.ErrBindingConflict):
			current, getErr := f.bindings.Get(ctx.Context(), s.Surface, s.Selected)
			if getErr != nil {
				return getErr
			}
			if current == nil {
				return f.renderList(ctx, s)
			}
			s.Wizard = ""
			return f.renderDetail(ctx, s, *current)
		default:
			return f.rearm(ctx, s, err.Error())
		}
	}
	s.Revision = updated.Revision
	s.Incarnation = updated.Incarnation
	s.Wizard = ""
	return f.renderDetail(ctx, s, updated)
}

var _ assistantinteraction.FeatureDriver = (*Feature)(nil)
var _ interface{ FeatureSpec() feature.Spec } = (*Feature)(nil)
