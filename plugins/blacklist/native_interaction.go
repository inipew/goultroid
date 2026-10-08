package blacklist

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
	"github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

const (
	nativeBlacklistScreen   = "group_rules"
	nativeBlacklistTTL      = 10 * time.Minute
	nativeBlacklistSlots    = 9
	nativeBlacklistPageSize = 5
)

type nativeBlacklistRuntimeState struct {
	mu      sync.RWMutex
	runtime nativeinteraction.DriverRuntime
}

type nativeBlacklistChoice struct {
	Kind string `json:"kind"`
	Word string `json:"word,omitempty"`
	Page int    `json:"page,omitempty"`
}

type nativeBlacklistState struct {
	Scope    nativeinteraction.GroupActionScope `json:"scope"`
	Page     int                                `json:"page"`
	Digest   string                             `json:"digest"`
	Selected string                             `json:"selected,omitempty"`
	Choices  []nativeBlacklistChoice            `json:"choices"`
}

var blacklistReadRequirement = core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator}
var blacklistWriteRequirement = core.GroupAuthorizationRequirement{
	Level:  core.GroupAuthorizationAdministrator,
	Rights: core.GroupAdminRights{DeleteMessages: true},
}

func (p *Plugin) FeatureSpec() feature.Spec {
	policy := feature.SudoPolicy(execution.SurfaceUserbot)
	policy.GroupOnly = true
	actions := []feature.Interaction{{ID: nativeBlacklistScreen, Kind: feature.InteractionScreen, Description: "Group blacklist manager", Surfaces: execution.SurfaceUserbot, Policy: policy}}
	for i := 0; i < nativeBlacklistSlots; i++ {
		actions = append(actions, feature.Interaction{ID: nativeBlacklistSlotID(i), Kind: feature.InteractionAction, Description: "Group blacklist menu action", Surfaces: execution.SurfaceUserbot, Policy: policy})
	}
	return feature.Spec{ID: p.Name(), Name: "Blacklist", Description: "Manage chat blacklist", Category: "Moderation", DurabilityVersion: "1", Interactions: actions}
}

func (p *Plugin) NativeFeatureID() string { return p.Name() }
func (*Plugin) NativeOptional() bool      { return true }
func nativeBlacklistSlotID(i int) string  { return fmt.Sprintf("slot_%02d", i) }

func (p *Plugin) BindNative(rt nativeinteraction.DriverRuntime) (func(), error) {
	if p == nil || p.db == nil || rt.Interactions == nil || rt.Catalog == nil || rt.Scope.IsZero() {
		return nil, nativeinteraction.ErrUnavailable
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope != rt.Scope {
		return nil, fmt.Errorf("blacklist: native feature scope unavailable")
	}
	registrations := make([]interface{ Close() }, 0, nativeBlacklistSlots)
	for i := 0; i < nativeBlacklistSlots; i++ {
		slot := i
		reg, err := rt.Interactions.RegisterAction(rt.Scope, p.Name(), nativeBlacklistSlotID(i), func(ctx *orchestration.Context) error { return p.handleNativeBlacklistChoice(ctx, slot) })
		if err != nil {
			for j := len(registrations) - 1; j >= 0; j-- {
				registrations[j].Close()
			}
			return nil, fmt.Errorf("blacklist: bind action %d: %w", i, err)
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

func (p *Plugin) currentNativeBlacklist() nativeinteraction.DriverRuntime {
	if p == nil {
		return nativeinteraction.DriverRuntime{}
	}
	p.native.mu.RLock()
	rt := p.native.runtime
	p.native.mu.RUnlock()
	return rt
}

func (p *Plugin) openNativeBlacklist(cmd *core.Context) (bool, error) {
	// Native menus are userbot-only. Assistant group commands retain their
	// existing transport and contextual authorization pipeline.
	if cmd == nil || cmd.IsAssistant() {
		return false, nil
	}
	rt := p.currentNativeBlacklist()
	if rt.Interactions == nil || rt.Scope.IsZero() || rt.Interactions.GroupRoleResolver() == nil {
		return false, nil
	}
	if cmd == nil || cmd.Chat == nil || !cmd.Chat.IsManagerGroup() || cmd.Message == nil || cmd.Message.ID <= 0 || cmd.SenderID() <= 0 || cmd.PeerID == nil {
		return true, core.ErrGroupOnly
	}
	scope := nativeinteraction.GroupActionScope{ChatID: cmd.ChatID(), Kind: cmd.Chat.Kind(), TopicID: cmd.TopicID()}
	target := presentationtelegram.MessageTarget{Peer: cmd.PeerID, ChatID: cmd.ChatID(), MessageID: cmd.Message.ID}
	session := interaction.Session{Binding: interaction.Binding{ActorID: cmd.SenderID(), ChatID: cmd.ChatID(), MessageID: cmd.Message.ID}}
	if err := nativeinteraction.AuthorizeFreshGroupAction(cmd.Ctx, session, target, scope, rt.Interactions.GroupRoleResolver(), blacklistReadRequirement); err != nil {
		return true, err
	}
	state := nativeBlacklistState{Scope: scope}
	data, view, err := p.nativeBlacklistView(cmd.Ctx, state)
	if err != nil {
		return true, err
	}
	_, err = rt.Interactions.Begin(cmd, nativeinteraction.BeginRequest{FeatureID: p.Name(), ScreenID: nativeBlacklistScreen, State: data, TTL: nativeBlacklistTTL, View: view})
	return true, err
}

// A canonical hash of the persisted rules detects changes between presentation
// and selection; comparison and writes happen under the existing chat rule lock.
func blacklistSnapshot(words []string) string {
	sorted := append([]string(nil), words...)
	sort.Strings(sorted)
	sum := sha256.New()
	for _, w := range sorted {
		_, _ = sum.Write([]byte(w))
		_, _ = sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (p *Plugin) nativeBlacklistView(ctx context.Context, state nativeBlacklistState) ([]byte, presentation.View, error) {
	if p == nil || p.db == nil {
		return nil, presentation.View{}, core.ErrUnavailable
	}
	if state.Scope.ChatID <= 0 || (state.Scope.Kind != core.ChatKindGroup && state.Scope.Kind != core.ChatKindSupergroup) {
		return nil, presentation.View{}, core.ErrGroupOnly
	}
	words, err := p.db.ListBlacklists(ctx, state.Scope.ChatID)
	if err != nil {
		return nil, presentation.View{}, err
	}
	sort.Strings(words)
	digest := blacklistSnapshot(words)
	if state.Digest != "" && state.Digest != digest {
		return nil, presentation.View{}, core.ErrConflict
	}
	state.Digest = digest
	state.Choices = nil
	rows := make([]presentation.Row, 0, 8)
	add := func(label string, choice nativeBlacklistChoice) (presentation.Button, error) {
		if len(state.Choices) >= nativeBlacklistSlots {
			return presentation.Button{}, core.ErrResourceLimit
		}
		n := len(state.Choices)
		state.Choices = append(state.Choices, choice)
		return presentation.Button{Text: label, ActionID: nativeBlacklistSlotID(n)}, nil
	}
	var body strings.Builder
	fmt.Fprintf(&body, "🚫 <b>Blacklist Grup</b>\nChat: <code>%d</code>\nAturan: <code>%d</code>\n", state.Scope.ChatID, len(words))
	if state.Selected != "" {
		found := false
		for _, w := range words {
			if w == state.Selected {
				found = true
				break
			}
		}
		if !found {
			return nil, presentation.View{}, core.ErrConflict
		}
		fmt.Fprintf(&body, "\nHapus <code>%s</code> dari grup ini?", html.EscapeString(state.Selected))
		yes, err := add("🗑 Confirm", nativeBlacklistChoice{Kind: "confirm"})
		if err != nil {
			return nil, presentation.View{}, err
		}
		no, err := add("↩ Back", nativeBlacklistChoice{Kind: "back"})
		if err != nil {
			return nil, presentation.View{}, err
		}
		rows = append(rows, presentation.Row{yes, no})
	} else {
		total := (len(words) + nativeBlacklistPageSize - 1) / nativeBlacklistPageSize
		if total < 1 {
			total = 1
		}
		if state.Page < 0 || state.Page >= total {
			state.Page = 0
		}
		fmt.Fprintf(&body, "\nHalaman %d/%d. Pilih aturan untuk dihapus.\n", state.Page+1, total)
		start := state.Page * nativeBlacklistPageSize
		end := start + nativeBlacklistPageSize
		if end > len(words) {
			end = len(words)
		}
		for _, w := range words[start:end] {
			label := []rune(w)
			if len(label) > 28 {
				label = append(label[:28], '…')
			}
			btn, err := add("✖ "+string(label), nativeBlacklistChoice{Kind: "select", Word: w})
			if err != nil {
				return nil, presentation.View{}, err
			}
			rows = append(rows, presentation.Row{btn})
		}
		if state.Page > 0 {
			btn, err := add("◀ Prev", nativeBlacklistChoice{Kind: "page", Page: state.Page - 1})
			if err != nil {
				return nil, presentation.View{}, err
			}
			rows = append(rows, presentation.Row{btn})
		}
		if state.Page+1 < total {
			btn, err := add("Next ▶", nativeBlacklistChoice{Kind: "page", Page: state.Page + 1})
			if err != nil {
				return nil, presentation.View{}, err
			}
			rows = append(rows, presentation.Row{btn})
		}
	}
	refresh, err := add("🔄 Refresh", nativeBlacklistChoice{Kind: "refresh"})
	if err != nil {
		return nil, presentation.View{}, err
	}
	closeButton, err := add("✖ Close", nativeBlacklistChoice{Kind: "close"})
	if err != nil {
		return nil, presentation.View{}, err
	}
	rows = append(rows, presentation.Row{refresh, closeButton})
	data, err := json.Marshal(state)
	if err != nil {
		return nil, presentation.View{}, err
	}
	view := presentation.View{Text: body.String(), Rows: rows}
	if err := view.Validate(); err != nil {
		return nil, presentation.View{}, err
	}
	return data, view, nil
}

func (p *Plugin) removeNativeBlacklistIfCurrent(ctx context.Context, chatID int64, word, digest string) error {
	if chatID <= 0 || word == "" || len(word) > MaxRuleBytes || digest == "" {
		return core.ErrInvalidArgs
	}
	lock := p.ruleLock(chatID)
	lock.Lock()
	defer lock.Unlock()
	words, err := p.db.ListBlacklists(ctx, chatID)
	if err != nil {
		return err
	}
	if blacklistSnapshot(words) != digest {
		return core.ErrConflict
	}
	found := false
	for _, w := range words {
		if w == word {
			found = true
			break
		}
	}
	if !found {
		return core.ErrNotFound
	}
	p.featureState.MarkUnknown(chatID)
	if err := p.db.RemoveBlacklist(ctx, chatID, word); err != nil {
		p.invalidateChatUnknown(chatID)
		return err
	}
	active := len(words) > 1
	p.invalidateChat(chatID, active)
	p.featureState.SetActive(chatID, active)
	return nil
}

func (p *Plugin) handleNativeBlacklistChoice(ctx *orchestration.Context, slot int) error {
	if p == nil || ctx == nil {
		return orchestration.ErrInvalidEngine
	}
	var state nativeBlacklistState
	if err := json.Unmarshal(ctx.State(), &state); err != nil || slot < 0 || slot >= len(state.Choices) {
		return ctx.Answer("Sesi blacklist tidak valid.", true)
	}
	choice := state.Choices[slot]
	if choice.Kind == "close" {
		return ctx.Terminate(presentation.View{Text: "🚫 Menu blacklist ditutup."})
	}
	rt := p.currentNativeBlacklist()
	if rt.Interactions == nil {
		return ctx.Answer("Menu blacklist tidak tersedia.", true)
	}
	requirement := blacklistReadRequirement
	if choice.Kind == "confirm" {
		requirement = blacklistWriteRequirement
	}
	if err := nativeinteraction.AuthorizeFreshGroupAction(ctx.Context(), ctx.Session(), ctx.Target(), state.Scope, rt.Interactions.GroupRoleResolver(), requirement); err != nil {
		return ctx.Answer("Hak administrator grup tidak dapat diverifikasi.", true)
	}
	switch choice.Kind {
	case "refresh":
		state.Digest = ""
		state.Selected = ""
	case "select":
		if choice.Word == "" || len(choice.Word) > MaxRuleBytes {
			return ctx.Answer("Aturan tidak valid.", true)
		}
		state.Selected = choice.Word
	case "back":
		state.Selected = ""
	case "page":
		if choice.Page < 0 || choice.Page > MaxRulesPerChat/nativeBlacklistPageSize {
			return ctx.Answer("Halaman tidak valid.", true)
		}
		state.Page = choice.Page
	case "confirm":
		if state.Selected == "" {
			return ctx.Answer("Konfirmasi tidak valid.", true)
		}
		if err := p.removeNativeBlacklistIfCurrent(ctx.Context(), state.Scope.ChatID, state.Selected, state.Digest); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return ctx.Answer("Daftar berubah. Refresh dan ulangi.", true)
			}
			return ctx.Answer("Gagal menghapus aturan blacklist.", true)
		}
		state.Digest = ""
		state.Selected = ""
	default:
		return ctx.Answer("Tombol tidak valid.", true)
	}
	data, view, err := p.nativeBlacklistView(ctx.Context(), state)
	if err != nil {
		if errors.Is(err, core.ErrConflict) {
			return ctx.Answer("Daftar berubah. Refresh dan ulangi.", true)
		}
		return ctx.Answer("Gagal memuat aturan blacklist.", true)
	}
	return ctx.Transition(data, nativeBlacklistTTL, view)
}

var _ nativeinteraction.OptionalFeatureDriver = (*Plugin)(nil)
