package filters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

const (
	nativeFiltersScreen   = "group_rules"
	nativeFiltersTTL      = 10 * time.Minute
	nativeFiltersSlots    = 9
	nativeFiltersPageSize = 5
)

type nativeFiltersRuntimeState struct {
	mu      sync.RWMutex
	runtime nativeinteraction.DriverRuntime
}
type nativeFiltersChoice struct {
	Kind    string `json:"kind"`
	Keyword string `json:"keyword,omitempty"`
	Page    int    `json:"page,omitempty"`
}
type nativeFiltersState struct {
	Scope    nativeinteraction.GroupActionScope `json:"scope"`
	Page     int                                `json:"page"`
	Digest   string                             `json:"digest"`
	Selected string                             `json:"selected,omitempty"`
	Choices  []nativeFiltersChoice              `json:"choices"`
}

var filtersManagerRequirement = core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator}

func (p *Plugin) FeatureSpec() feature.Spec {
	policy := feature.SudoPolicy(execution.SurfaceUserbot)
	policy.GroupOnly = true
	actions := []feature.Interaction{{ID: nativeFiltersScreen, Kind: feature.InteractionScreen, Description: "Chat-scoped filters management", Surfaces: execution.SurfaceUserbot, Policy: policy}}
	for i := 0; i < nativeFiltersSlots; i++ {
		actions = append(actions, feature.Interaction{ID: nativeFiltersSlotID(i), Kind: feature.InteractionAction, Description: "Group filters action slot", Surfaces: execution.SurfaceUserbot, Policy: policy})
	}
	return feature.Spec{ID: p.Name(), Name: "Filters", Description: "Manage group keyword responses", Category: "Filters", DurabilityVersion: "1", Interactions: actions}
}
func (p *Plugin) NativeFeatureID() string { return p.Name() }
func (*Plugin) NativeOptional() bool      { return true }
func nativeFiltersSlotID(i int) string    { return fmt.Sprintf("slot_%02d", i) }

func (p *Plugin) BindNative(rt nativeinteraction.DriverRuntime) (func(), error) {
	if p == nil || p.db == nil || p.responses == nil || rt.Interactions == nil || rt.Catalog == nil || rt.Scope.IsZero() {
		return nil, nativeinteraction.ErrUnavailable
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope != rt.Scope {
		return nil, fmt.Errorf("filters: native feature scope unavailable")
	}
	registrations := make([]interface{ Close() }, 0, nativeFiltersSlots)
	for i := 0; i < nativeFiltersSlots; i++ {
		slot := i
		reg, err := rt.Interactions.RegisterAction(rt.Scope, p.Name(), nativeFiltersSlotID(i), func(ctx *orchestration.Context) error { return p.handleNativeFiltersChoice(ctx, slot) })
		if err != nil {
			for j := len(registrations) - 1; j >= 0; j-- {
				registrations[j].Close()
			}
			return nil, fmt.Errorf("filters: register action %d: %w", i, err)
		}
		registrations = append(registrations, reg)
	}
	p.native.mu.Lock()
	p.native.runtime = rt
	p.native.mu.Unlock()
	return func() {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
		p.native.mu.Lock()
		if p.native.runtime.Interactions == rt.Interactions && p.native.runtime.Scope == rt.Scope {
			p.native.runtime = nativeinteraction.DriverRuntime{}
		}
		p.native.mu.Unlock()
	}, nil
}

func (p *Plugin) currentNativeFilters() nativeinteraction.DriverRuntime {
	if p == nil {
		return nativeinteraction.DriverRuntime{}
	}
	p.native.mu.RLock()
	rt := p.native.runtime
	p.native.mu.RUnlock()
	return rt
}

// Native UI is only a verified userbot group surface. Assistant moderation and
// adapter-free commands keep their existing text response and authorization.
func (p *Plugin) openNativeFilters(cmd *core.Context) (bool, error) {
	if cmd == nil || cmd.IsAssistant() {
		return false, nil
	}
	rt := p.currentNativeFilters()
	if rt.Interactions == nil || rt.Scope.IsZero() || rt.Interactions.GroupRoleResolver() == nil {
		return false, nil
	}
	if cmd.Chat == nil || !cmd.Chat.IsManagerGroup() || cmd.Message == nil || cmd.Message.ID <= 0 || cmd.SenderID() <= 0 || cmd.PeerID == nil {
		return true, core.ErrGroupOnly
	}
	scope := nativeinteraction.GroupActionScope{ChatID: cmd.ChatID(), Kind: cmd.Chat.Kind(), TopicID: cmd.TopicID()}
	target := presentationtelegram.MessageTarget{Peer: cmd.PeerID, ChatID: cmd.ChatID(), MessageID: cmd.Message.ID}
	session := rootinteraction.Session{Binding: rootinteraction.Binding{ActorID: cmd.SenderID(), ChatID: cmd.ChatID(), MessageID: cmd.Message.ID}}
	if err := nativeinteraction.AuthorizeFreshGroupAction(cmd.Ctx, session, target, scope, rt.Interactions.GroupRoleResolver(), filtersManagerRequirement); err != nil {
		return true, err
	}
	raw, view, err := p.nativeFiltersView(cmd.Ctx, nativeFiltersState{Scope: scope})
	if err != nil {
		return true, err
	}
	_, err = rt.Interactions.Begin(cmd, nativeinteraction.BeginRequest{FeatureID: p.Name(), ScreenID: nativeFiltersScreen, State: raw, TTL: nativeFiltersTTL, View: view})
	return true, err
}

// Snapshot includes the response body, formatting, media metadata and time,
// so replacing a response under the same keyword invalidates old previews.
func filtersSnapshot(filters []Filter) (string, error) {
	sorted := append([]Filter(nil), filters...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Keyword < sorted[j].Keyword })
	payload, err := json.Marshal(sorted)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func (p *Plugin) nativeFiltersView(ctx context.Context, state nativeFiltersState) ([]byte, presentation.View, error) {
	if p == nil || p.db == nil {
		return nil, presentation.View{}, core.ErrUnavailable
	}
	if state.Scope.ChatID <= 0 || (state.Scope.Kind != core.ChatKindGroup && state.Scope.Kind != core.ChatKindSupergroup) {
		return nil, presentation.View{}, core.ErrGroupOnly
	}
	filters, err := p.db.ListFilters(ctx, state.Scope.ChatID)
	if err != nil {
		return nil, presentation.View{}, err
	}
	sort.Slice(filters, func(i, j int) bool { return filters[i].Keyword < filters[j].Keyword })
	digest, err := filtersSnapshot(filters)
	if err != nil {
		return nil, presentation.View{}, err
	}
	if state.Digest != "" && state.Digest != digest {
		return nil, presentation.View{}, core.ErrConflict
	}
	state.Digest = digest
	state.Choices = nil
	rows := make([]presentation.Row, 0, 8)
	add := func(label string, choice nativeFiltersChoice) (presentation.Button, error) {
		if len(state.Choices) >= nativeFiltersSlots {
			return presentation.Button{}, core.ErrResourceLimit
		}
		index := len(state.Choices)
		state.Choices = append(state.Choices, choice)
		return presentation.Button{Text: label, ActionID: nativeFiltersSlotID(index)}, nil
	}
	var body strings.Builder
	fmt.Fprintf(&body, "🧩 <b>Filters Grup</b>\nChat: <code>%d</code>\nAturan: <code>%d</code>\n", state.Scope.ChatID, len(filters))
	if state.Selected != "" {
		var selected *Filter
		for i := range filters {
			if filters[i].Keyword == state.Selected {
				selected = &filters[i]
				break
			}
		}
		if selected == nil {
			return nil, presentation.View{}, core.ErrConflict
		}
		preview := []rune(selected.Response.Text)
		if len(preview) > 180 {
			preview = append(preview[:180], '…')
		}
		fmt.Fprintf(&body, "\n<b>%s</b>\nRespons: <code>%s</code>\n", html.EscapeString(selected.Keyword), html.EscapeString(string(preview)))
		if media := selected.Response.Media; media != nil {
			fmt.Fprintf(&body, "Media: <code>%s</code>\n", html.EscapeString(media.MediaType))
		}
		fmt.Fprint(&body, "\nKonfirmasi hapus filter dari grup ini?")
		yes, err := add("🗑 Confirm", nativeFiltersChoice{Kind: "confirm"})
		if err != nil {
			return nil, presentation.View{}, err
		}
		back, err := add("↩ Back", nativeFiltersChoice{Kind: "back"})
		if err != nil {
			return nil, presentation.View{}, err
		}
		rows = append(rows, presentation.Row{yes, back})
	} else {
		total := (len(filters) + nativeFiltersPageSize - 1) / nativeFiltersPageSize
		if total < 1 {
			total = 1
		}
		if state.Page < 0 || state.Page >= total {
			state.Page = 0
		}
		fmt.Fprintf(&body, "\nHalaman %d/%d. Pilih filter untuk melihat lalu menghapus.\n", state.Page+1, total)
		from := state.Page * nativeFiltersPageSize
		to := from + nativeFiltersPageSize
		if to > len(filters) {
			to = len(filters)
		}
		for _, filter := range filters[from:to] {
			label := []rune(filter.Keyword)
			if len(label) > 28 {
				label = append(label[:28], '…')
			}
			button, err := add("🔎 "+string(label), nativeFiltersChoice{Kind: "select", Keyword: filter.Keyword})
			if err != nil {
				return nil, presentation.View{}, err
			}
			rows = append(rows, presentation.Row{button})
		}
		if state.Page > 0 {
			button, err := add("◀ Prev", nativeFiltersChoice{Kind: "page", Page: state.Page - 1})
			if err != nil {
				return nil, presentation.View{}, err
			}
			rows = append(rows, presentation.Row{button})
		}
		if state.Page+1 < total {
			button, err := add("Next ▶", nativeFiltersChoice{Kind: "page", Page: state.Page + 1})
			if err != nil {
				return nil, presentation.View{}, err
			}
			rows = append(rows, presentation.Row{button})
		}
	}
	refresh, err := add("🔄 Refresh", nativeFiltersChoice{Kind: "refresh"})
	if err != nil {
		return nil, presentation.View{}, err
	}
	closeButton, err := add("✖ Close", nativeFiltersChoice{Kind: "close"})
	if err != nil {
		return nil, presentation.View{}, err
	}
	rows = append(rows, presentation.Row{refresh, closeButton})
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, presentation.View{}, err
	}
	view := presentation.View{Text: body.String(), Rows: rows}
	if err := view.Validate(); err != nil {
		return nil, presentation.View{}, err
	}
	return raw, view, nil
}

// Auth is verified *inside* the existing per-chat write lock and directly
// before CommitDelete. This closes the preview/demotion window for this write.
func (p *Plugin) removeNativeFilterIfCurrent(ctx context.Context, session rootinteraction.Session, target presentation.Target, scope nativeinteraction.GroupActionScope, keyword, digest string, roles core.GroupRoleResolver) error {
	if p == nil || p.db == nil || p.responses == nil {
		return core.ErrUnavailable
	}
	if scope.ChatID <= 0 || keyword == "" || len(keyword) > MaxKeywordBytes || digest == "" {
		return core.ErrInvalidArgs
	}
	lock := p.ruleLock(scope.ChatID)
	lock.Lock()
	defer lock.Unlock()
	filters, err := p.db.ListFilters(ctx, scope.ChatID)
	if err != nil {
		return err
	}
	current, err := filtersSnapshot(filters)
	if err != nil {
		return err
	}
	if digest != current {
		return core.ErrConflict
	}
	var selected *Filter
	for i := range filters {
		if filters[i].Keyword == keyword {
			selected = &filters[i]
			break
		}
	}
	if selected == nil {
		return core.ErrNotFound
	}
	if err := nativeinteraction.AuthorizeFreshGroupAction(ctx, session, target, scope, roles, filtersManagerRequirement); err != nil {
		return err
	}
	p.featureState.MarkUnknown(scope.ChatID)
	if err := p.responses.CommitDelete(ctx, selected.Response, func() error { return p.db.DeleteFilter(ctx, scope.ChatID, keyword) }); err != nil {
		p.invalidateChatUnknown(scope.ChatID)
		return err
	}
	active := len(filters) > 1
	p.invalidateChat(scope.ChatID, active)
	p.featureState.SetActive(scope.ChatID, active)
	return nil
}

func (p *Plugin) handleNativeFiltersChoice(ctx *orchestration.Context, slot int) error {
	if p == nil || ctx == nil {
		return orchestration.ErrInvalidEngine
	}
	var state nativeFiltersState
	if err := json.Unmarshal(ctx.State(), &state); err != nil || slot < 0 || slot >= len(state.Choices) {
		return ctx.Answer("Sesi filters tidak valid.", true)
	}
	choice := state.Choices[slot]
	if choice.Kind == "close" {
		return ctx.Terminate(presentation.View{Text: "🧩 Menu filters ditutup."})
	}
	rt := p.currentNativeFilters()
	if rt.Interactions == nil {
		return ctx.Answer("Menu filters tidak tersedia.", true)
	}
	// Mutation authorization is checked after taking the write lock, immediately
	// before CommitDelete. Non-mutating actions also require a fresh role.
	if choice.Kind != "confirm" {
		if err := nativeinteraction.AuthorizeFreshGroupAction(ctx.Context(), ctx.Session(), ctx.Target(), state.Scope, rt.Interactions.GroupRoleResolver(), filtersManagerRequirement); err != nil {
			return ctx.Answer("Hak administrator grup tidak dapat diverifikasi.", true)
		}
	}
	switch choice.Kind {
	case "refresh":
		state.Digest = ""
		state.Selected = ""
	case "select":
		if choice.Keyword == "" || len(choice.Keyword) > MaxKeywordBytes {
			return ctx.Answer("Keyword tidak valid.", true)
		}
		state.Selected = choice.Keyword
	case "back":
		state.Selected = ""
	case "page":
		if choice.Page < 0 || choice.Page > MaxRulesPerChat/nativeFiltersPageSize {
			return ctx.Answer("Halaman tidak valid.", true)
		}
		state.Page = choice.Page
	case "confirm":
		if state.Selected == "" {
			return ctx.Answer("Konfirmasi tidak valid.", true)
		}
		if err := p.removeNativeFilterIfCurrent(ctx.Context(), ctx.Session(), ctx.Target(), state.Scope, state.Selected, state.Digest, rt.Interactions.GroupRoleResolver()); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return ctx.Answer("Daftar filter berubah. Refresh lalu ulangi.", true)
			}
			return ctx.Answer("Tidak dapat menghapus filter atau hak admin dicabut.", true)
		}
		state.Digest = ""
		state.Selected = ""
	default:
		return ctx.Answer("Aksi tidak valid.", true)
	}
	raw, view, err := p.nativeFiltersView(ctx.Context(), state)
	if err != nil {
		if errors.Is(err, core.ErrConflict) {
			return ctx.Answer("Daftar filter berubah. Refresh lalu ulangi.", true)
		}
		return ctx.Answer("Gagal memuat filters.", true)
	}
	return ctx.Transition(raw, nativeFiltersTTL, view)
}

var _ nativeinteraction.OptionalFeatureDriver = (*Plugin)(nil)
