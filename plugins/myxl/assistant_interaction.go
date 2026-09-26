package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/ui"
)

const (
	assistantTTL             = 24 * time.Hour
	assistantInputTTL        = 2 * time.Minute
	assistantConfirmationTTL = purchaseConfirmationTTL
	assistantActionSlotCount = 32

	assistantScreenHome     = "home"
	assistantScreenInput    = "input"
	assistantScreenCheckout = "checkout"
)

type assistantState struct {
	Slots      []string            `json:"slots,omitempty"`
	Wizard     string              `json:"wizard,omitempty"`
	MSISDN     string              `json:"msisdn,omitempty"`
	OptionCode string              `json:"option_code,omitempty"`
	Method     string              `json:"method,omitempty"`
	Draft      *purchaseIntentState `json:"draft,omitempty"`
	Sustain    bool                `json:"sustain,omitempty"`
}

func (p *Plugin) AssistantFeatureID() string { return p.Name() }

func (p *Plugin) FeatureSpec() feature.Spec {
	policy := feature.OwnerPolicy(execution.SurfaceAssistant)
	policy.PrivateOnly = true
	interactions := []feature.Interaction{
		{
			ID:          assistantScreenHome,
			Kind:        feature.InteractionScreen,
			Description: "MyXL interactive dashboard",
			Surfaces:    execution.SurfaceAssistant,
			Policy:      policy,
		},
		{
			ID:          assistantScreenInput,
			Kind:        feature.InteractionScreen,
			Description: "MyXL bounded free-form input",
			Surfaces:    execution.SurfaceAssistant,
			Policy:      policy,
		},
		{
			ID:          assistantScreenCheckout,
			Kind:        feature.InteractionScreen,
			Description: "MyXL direct purchase checkout",
			Surfaces:    execution.SurfaceAssistant,
			Policy:      policy,
		},
	}
	for i := 0; i < assistantActionSlotCount; i++ {
		interactions = append(interactions, feature.Interaction{
			ID:          assistantSlotID(i),
			Kind:        feature.InteractionAction,
			Description: "MyXL session-bound action slot",
			Surfaces:    execution.SurfaceAssistant,
			Policy:      policy,
		})
	}

	nativePolicy := feature.OwnerPolicy(execution.SurfaceUserbot)
	interactions = append(interactions,
		feature.Interaction{
			ID:          nativeQuotaScreen,
			Kind:        feature.InteractionScreen,
			Description: "Native userbot quota refresh canary",
			Surfaces:    execution.SurfaceUserbot,
			Policy:      nativePolicy,
		},
		feature.Interaction{
			ID:          nativeQuotaRefreshAction,
			Kind:        feature.InteractionAction,
			Description: "Refresh native userbot MyXL quota",
			Surfaces:    execution.SurfaceUserbot,
			Policy:      nativePolicy,
		},
		feature.Interaction{
			ID:          nativePurchaseScreen,
			Kind:        feature.InteractionScreen,
			Description: "Native userbot MyXL purchase confirmation",
			Surfaces:    execution.SurfaceUserbot,
			Policy:      nativePolicy,
		},
		feature.Interaction{
			ID:          nativePurchaseConfirmAction,
			Kind:        feature.InteractionAction,
			Description: "Confirm native userbot MyXL purchase",
			Surfaces:    execution.SurfaceUserbot,
			Policy:      nativePolicy,
		},
		feature.Interaction{
			ID:          nativePurchaseCancelAction,
			Kind:        feature.InteractionAction,
			Description: "Cancel native userbot MyXL purchase",
			Surfaces:    execution.SurfaceUserbot,
			Policy:      nativePolicy,
		},
	)
	return feature.Spec{
		ID:           p.Name(),
		Name:         "MyXL",
		Description:  p.Description(),
		Category:     "Utility",
		Interactions: interactions,
	}
}

func assistantSlotID(index int) string {
	return fmt.Sprintf("slot_%02d", index)
}

func (p *Plugin) BindAssistant(rt assistantinteraction.DriverRuntime) (func(), error) {
	if p == nil || rt.Engine == nil || rt.Catalog == nil || rt.Admit == nil {
		return nil, orchestration.ErrInvalidEngine
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope.IsZero() {
		return nil, fmt.Errorf("myxl: assistant feature scope unavailable")
	}

	registrations := make([]interface{ Close() }, 0, assistantActionSlotCount)
	for i := 0; i < assistantActionSlotCount; i++ {
		slot := i
		actionID := assistantSlotID(slot)
		reg, err := rt.Engine.RegisterAction(scope, p.Name(), actionID, func(ctx *orchestration.Context) error {
			if ctx == nil {
				return orchestration.ErrInvalidEngine
			}
			session := ctx.Session()
			if err := rt.Admit(p.Name(), feature.InteractionAction, actionID, session.Binding.ActorID, ctx.Target()); err != nil {
				return err
			}
			return p.handleAssistantSlot(ctx, slot)
		})
		if err != nil {
			for j := len(registrations) - 1; j >= 0; j-- {
				registrations[j].Close()
			}
			return nil, fmt.Errorf("myxl: register assistant action %s: %w", actionID, err)
		}
		registrations = append(registrations, reg)
	}

	p.assistantMu.Lock()
	p.assistantRuntime = rt
	p.assistantMu.Unlock()

	cleanup := func() {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
		p.assistantMu.Lock()
		if p.assistantRuntime.Engine == rt.Engine {
			p.assistantRuntime = assistantinteraction.DriverRuntime{}
		}
		p.assistantMu.Unlock()
	}
	return cleanup, nil
}

func (p *Plugin) currentAssistantRuntime() assistantinteraction.DriverRuntime {
	if p == nil {
		return assistantinteraction.DriverRuntime{}
	}
	p.assistantMu.RLock()
	rt := p.assistantRuntime
	p.assistantMu.RUnlock()
	return rt
}

func (p *Plugin) openAssistant(cmd *core.Context) error {
	if p == nil || cmd == nil || cmd.PeerID == nil || cmd.SenderID() == 0 {
		return fmt.Errorf("myxl: assistant command target unavailable")
	}
	rt := p.currentAssistantRuntime()
	if rt.Engine == nil || rt.Admit == nil {
		return fmt.Errorf("myxl: assistant runtime unavailable")
	}
	chatID := cmd.ChatID()
	if chatID == 0 {
		chatID = cmd.SenderID()
	}
	target := presentationtelegram.MessageTarget{Peer: cmd.PeerID, ChatID: chatID}
	if err := rt.Admit(p.Name(), feature.InteractionScreen, assistantScreenHome, cmd.SenderID(), target); err != nil {
		return err
	}
	screen, err := p.menuMgr.BuildDashboardScreen(cmd.Ctx, false)
	if err != nil {
		return err
	}
	state, view, err := p.assistantScreen(assistantState{Sustain: true}, screen)
	if err != nil {
		return err
	}
	_, err = rt.Engine.Begin(cmd.Ctx, orchestration.BeginRequest{
		FeatureID: p.Name(),
		ActorID:   cmd.SenderID(),
		State:     state,
		TTL:       assistantTTL,
		Target:    target,
		View:      view,
	})
	return err
}

func (p *Plugin) openAssistantPurchaseConfirmation(
	cmd *core.Context,
	intent purchaseIntentState,
	quote purchaseCheckoutPreview,
) error {
	if p == nil || cmd == nil || cmd.PeerID == nil || cmd.SenderID() == 0 {
		return fmt.Errorf("myxl: assistant purchase target unavailable")
	}
	rt := p.currentAssistantRuntime()
	if rt.Engine == nil || rt.Admit == nil {
		return fmt.Errorf("myxl: assistant runtime unavailable")
	}
	intent, err := normalizePurchaseIntent(intent)
	if err != nil {
		return err
	}
	chatID := cmd.ChatID()
	if chatID == 0 {
		chatID = cmd.SenderID()
	}
	target := presentationtelegram.MessageTarget{Peer: cmd.PeerID, ChatID: chatID}
	if err := rt.Admit(p.Name(), feature.InteractionScreen, assistantScreenCheckout, cmd.SenderID(), target); err != nil {
		return err
	}
	screen, err := p.menuMgr.BuildCheckoutScreen(quote)
	if err != nil {
		return err
	}
	state, view, err := p.assistantScreen(assistantState{
		Draft:   &intent,
		Sustain: false,
	}, screen)
	if err != nil {
		return err
	}
	_, err = rt.Engine.Begin(cmd.Ctx, orchestration.BeginRequest{
		FeatureID: p.Name(),
		ActorID:   cmd.SenderID(),
		State:     state,
		TTL:       assistantConfirmationTTL,
		Target:    target,
		View:      view,
	})
	return err
}

func decodeAssistantState(raw []byte) assistantState {
	var state assistantState
	if len(raw) == 0 {
		return state
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return assistantState{}
	}
	return state
}

func encodeAssistantState(state assistantState) ([]byte, error) {
	return json.Marshal(state)
}

func (p *Plugin) assistantScreen(state assistantState, screen *ui.Screen) ([]byte, presentation.View, error) {
	if screen == nil {
		return nil, presentation.View{}, fmt.Errorf("myxl: nil screen")
	}
	state.Slots = state.Slots[:0]
	rows := make([]presentation.Row, 0, len(screen.Rows))
	slot := 0
	for _, row := range screen.Rows {
		out := make(presentation.Row, 0, len(row))
		for _, button := range row {
			if button.Type != ui.ButtonCallback {
				return nil, presentation.View{}, fmt.Errorf("myxl: unsupported assistant button type %d", button.Type)
			}
			if slot >= assistantActionSlotCount {
				return nil, presentation.View{}, fmt.Errorf("myxl: assistant screen exceeds %d action slots", assistantActionSlotCount)
			}
			state.Slots = append(state.Slots, string(button.Data))
			out = append(out, presentation.Button{
				Text:     button.Text,
				ActionID: assistantSlotID(slot),
			})
			slot++
		}
		if len(out) > 0 {
			rows = append(rows, out)
		}
	}
	raw, err := encodeAssistantState(state)
	if err != nil {
		return nil, presentation.View{}, err
	}
	view := presentation.View{Text: screen.Text(), Rows: rows}
	if err := view.Validate(); err != nil {
		return nil, presentation.View{}, err
	}
	return raw, view, nil
}

func (p *Plugin) assistantTransition(ctx *orchestration.Context, state assistantState, screen *ui.Screen) error {
	return p.assistantTransitionWithTTL(ctx, state, assistantTTL, screen)
}

func (p *Plugin) assistantTransitionWithTTL(ctx *orchestration.Context, state assistantState, ttl time.Duration, screen *ui.Screen) error {
	state.Wizard = ""
	state.Sustain = ttl == assistantTTL
	raw, view, err := p.assistantScreen(state, screen)
	if err != nil {
		return err
	}
	return ctx.Transition(raw, ttl, view)
}

func assistantDestructiveConfirmation(
	state assistantState,
	text string,
	confirmIntent string,
	cancelIntent string,
	confirmLabel string,
) ([]byte, presentation.View, error) {
	state.Wizard = ""
	state.Sustain = false
	state.Slots = []string{confirmIntent, cancelIntent}
	raw, err := encodeAssistantState(state)
	if err != nil {
		return nil, presentation.View{}, err
	}
	if strings.TrimSpace(confirmLabel) == "" {
		confirmLabel = "🗑️ Ya, Lanjutkan"
	}
	return raw, presentation.View{
		Text: text,
		Rows: []presentation.Row{{
			{Text: confirmLabel, ActionID: assistantSlotID(0)},
			{Text: "❌ Batal", ActionID: assistantSlotID(1)},
		}},
	}, nil
}

func (p *Plugin) assistantAwait(ctx *orchestration.Context, state assistantState, prompt string) error {
	state.Sustain = false
	state.Slots = []string{"myxl:cancel_wizard"}
	raw, err := encodeAssistantState(state)
	if err != nil {
		return err
	}
	view := presentation.View{
		Text: prompt,
		Rows: []presentation.Row{{
			{Text: "❌ Batal", ActionID: assistantSlotID(0)},
		}},
	}
	return ctx.AwaitInput(raw, assistantInputTTL, view)
}

func parseAssistantIntent(data string) (namespace, action, opaque string, err error) {
	parts := strings.SplitN(strings.TrimSpace(data), ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" || parts[0] == "a1" {
		return "", "", "", fmt.Errorf("myxl: invalid assistant action intent")
	}
	opaque = "noop"
	if len(parts) == 3 {
		if parts[2] == "" {
			return "", "", "", fmt.Errorf("myxl: invalid empty assistant action payload")
		}
		opaque = parts[2]
	}
	return parts[0], parts[1], opaque, nil
}

func (p *Plugin) handleAssistantSlot(ctx *orchestration.Context, slot int) error {
	state := decodeAssistantState(ctx.State())
	if slot < 0 || slot >= len(state.Slots) {
		return ctx.Answer("Interaction expired. Reopen MyXL.", true)
	}
	namespace, action, opaque, err := parseAssistantIntent(state.Slots[slot])
	if err != nil {
		return ctx.Answer("Interaction data is invalid. Reopen MyXL.", true)
	}
	if namespace == "assistant" && action == "close" {
		if err := ctx.Terminate(presentation.View{Text: "✅ Menu MyXL ditutup."}); err != nil {
			return err
		}
		return ctx.Answer("", false)
	}
	if namespace != p.Name() {
		return ctx.Answer("Interaction owner mismatch. Reopen MyXL.", true)
	}
	if state.Sustain {
		if err := ctx.Touch(assistantTTL); err != nil {
			return err
		}
	}
	return p.dispatchAssistantAction(ctx, state, action, opaque)
}

func (p *Plugin) dispatchAssistantAction(ctx *orchestration.Context, state assistantState, action, opaque string) error {
	switch action {
	case "home", "refresh":
		screen, err := p.menuMgr.BuildDashboardScreen(ctx.Context(), false)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat MyXL: %v", err), true)
		}
		if action == "refresh" {
			_ = ctx.Answer("🔄 Kuota & pulsa diperbarui", false)
		}
		return p.assistantTransition(ctx, state, screen)

	case "detail", "quota":
		screen, err := p.menuMgr.BuildQuotaDetailScreen(ctx.Context(), false)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat rincian: %v", err), true)
		}
		return p.assistantTransition(ctx, state, screen)

	case "accounts":
		screen, err := p.menuMgr.BuildAccountsScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat akun: %v", err), true)
		}
		return p.assistantTransition(ctx, state, screen)

	case "switch":
		if opaque == "" || opaque == "noop" {
			return ctx.Answer("", false)
		}
		if err := p.repo.SetActive(ctx.Context(), opaque); err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal ganti akun: %v", err), true)
		}
		_ = ctx.Answer("✅ Akun aktif diganti", false)
		screen, err := p.menuMgr.BuildAccountsScreen(ctx.Context())
		if err != nil {
			return err
		}
		return p.assistantTransition(ctx, state, screen)

	case "alias_pick":
		screen, err := p.menuMgr.BuildAliasPickScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat akun: %v", err), true)
		}
		return p.assistantTransition(ctx, state, screen)

	case "alias_req":
		state.Wizard = "alias"
		state.MSISDN = opaque
		prompt := fmt.Sprintf(
			"🏷️ <b>Ubah Alias Akun</b>\n\nNomor: <code>%s</code>\n\nSilakan kirimkan nama alias baru (maksimal 24 karakter):\n\n<i>Ketik <code>/cancel</code> untuk membatalkan.</i>",
			html.EscapeString(opaque),
		)
		if err := p.assistantAwait(ctx, state, prompt); err != nil {
			return err
		}
		return ctx.Answer("Ketik nama alias baru…", false)

	case "del_pick":
		screen, err := p.menuMgr.BuildDeletePickScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat akun: %v", err), true)
		}
		return p.assistantTransition(ctx, state, screen)

	case "del_ask":
		raw, view, err := assistantDestructiveConfirmation(
			state,
			fmt.Sprintf("⚠️ <b>Hapus Akun MyXL</b>\n\nApakah Anda yakin ingin menghapus nomor <code>%s</code> dari penyimpanan bot?", html.EscapeString(opaque)),
			fmt.Sprintf("myxl:del_exec:%s", opaque),
			"myxl:accounts",
			"🗑️ Ya, Hapus",
		)
		if err != nil {
			return err
		}
		return ctx.Transition(raw, assistantConfirmationTTL, view)

	case "del_exec":
		if err := p.repo.Delete(ctx.Context(), opaque); err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal menghapus: %v", err), true)
		}
		_ = ctx.Answer("✅ Akun berhasil dihapus", false)
		screen, err := p.menuMgr.BuildAccountsScreen(ctx.Context())
		if err != nil {
			return err
		}
		return p.assistantTransition(ctx, state, screen)

	case "token_refresh":
		acc, err := p.repo.GetActive(ctx.Context())
		if err != nil || acc == nil {
			return ctx.Answer("Tidak ada akun aktif", true)
		}
		if err := p.client.EnsureFreshToken(ctx.Context(), acc); err != nil {
			return ctx.Answer(fmt.Sprintf("Refresh token gagal: %v", err), true)
		}
		_ = ctx.Answer("🔄 Token CIAM berhasil disegarkan", false)
		screen, err := p.menuMgr.BuildAccountsScreen(ctx.Context())
		if err != nil {
			return err
		}
		return p.assistantTransition(ctx, state, screen)

	case "login_req":
		state.Wizard = "login_msisdn"
		state.MSISDN = ""
		prompt := "📱 <b>Login MyXL — Langkah 1 dari 2</b>\n\n" +
			"Masukkan nomor HP XL/Axis yang ingin didaftarkan.\n" +
			"Format: <code>0819...</code> atau <code>62819...</code>\n\n" +
			"<i>Ketik <code>/cancel</code> atau tekan Batal di bawah untuk membatalkan.</i>"
		if err := p.assistantAwait(ctx, state, prompt); err != nil {
			return err
		}
		return ctx.Answer("Kirimkan nomor HP Anda…", false)

	case "resend_otp":
		msisdn := opaque
		if msisdn == "" || msisdn == "noop" {
			msisdn = state.MSISDN
		}
		if msisdn == "" {
			return ctx.Answer("Nomor HP tidak valid", true)
		}
		subID, err := p.client.RequestOTP(ctx.Context(), msisdn)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal kirim ulang OTP: %v", err), true)
		}
		if subID != "" {
			if existing, _ := p.repo.GetByMSISDN(ctx.Context(), msisdn); existing != nil {
				existing.SubscriberID = subID
				_ = p.repo.Save(ctx.Context(), existing)
			}
		}
		return ctx.Answer("📩 Kode OTP telah dikirim ulang via SMS!", true)

	case "cancel_wizard":
		screen, err := p.menuMgr.BuildDashboardScreen(ctx.Context(), false)
		if err != nil {
			return err
		}
		_ = ctx.Answer("Wizard dibatalkan", false)
		return p.assistantTransition(ctx, assistantState{}, screen)

	case "store":
		screen, err := p.menuMgr.BuildStoreScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat store: %v", err), true)
		}
		return p.assistantTransition(ctx, state, screen)

	case "saved":
		screen, err := p.menuMgr.BuildSavedPackagesScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat favorit: %v", err), true)
		}
		return p.assistantTransition(ctx, state, screen)

	case "fam_input":
		state.Wizard = "family"
		prompt := "🔍 <b>Input Family Code Paket</b>\n\n" +
			"Silakan kirimkan Family Code paket yang ingin Anda telusuri (contoh: <code>7658c955-a0b9-405f-bb17-de7f43d1a946</code>):\n\n" +
			"<i>Ketik <code>/cancel</code> untuk membatalkan.</i>"
		if err := p.assistantAwait(ctx, state, prompt); err != nil {
			return err
		}
		return ctx.Answer("Kirimkan Family Code…", false)

	case "fam_page":
		parts := strings.Split(opaque, ":")
		if len(parts) < 2 {
			return ctx.Answer("Data halaman tidak lengkap", true)
		}
		page, _ := strconv.Atoi(parts[len(parts)-1])
		if page < 1 {
			page = 1
		}
		familyCode := strings.Join(parts[:len(parts)-1], ":")
		acc, err := p.repo.GetActive(ctx.Context())
		if err != nil || acc == nil {
			return ctx.Answer("Tidak ada akun aktif", true)
		}
		screen, err := p.menuMgr.BuildFamilyPackagesScreen(ctx.Context(), acc, familyCode, page)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat paket family: %v", err), true)
		}
		return p.assistantTransition(ctx, state, screen)

	case "buy_opt_input":
		state.Wizard = "option_code"
		prompt := "⚡ <b>Input Option Code Paket</b>\n\n" +
			"Silakan kirimkan kode paket yang ingin Anda beli (contoh: <code>OPT12345</code>):\n\n" +
			"<i>Ketik <code>/cancel</code> untuk membatalkan.</i>"
		if err := p.assistantAwait(ctx, state, prompt); err != nil {
			return err
		}
		return ctx.Answer("Kirimkan kode paket…", false)

	case "buy_opt":
		optionCode := p.menuMgr.ResolveOptionCode(opaque)
		if optionCode == "" {
			return ctx.Answer("Kode paket tidak valid", true)
		}
		acc, err := p.repo.GetActive(ctx.Context())
		if err != nil || acc == nil {
			return ctx.Answer("Tidak ada akun aktif", true)
		}
		screen, err := p.menuMgr.BuildPackageDetailScreen(ctx.Context(), acc, optionCode)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat paket: %v", err), true)
		}
		return p.assistantTransition(ctx, state, screen)

	case "method":
		parts := strings.SplitN(opaque, ":", 2)
		if len(parts) != 2 {
			return ctx.Answer("Data metode tidak lengkap", true)
		}
		method := parts[0]
		optionCode := p.menuMgr.ResolveOptionCode(parts[1])
		acc, err := p.repo.GetActive(ctx.Context())
		if err != nil || acc == nil {
			return ctx.Answer("Tidak ada akun aktif", true)
		}
		intent, quote, err := p.preparePurchaseIntent(ctx.Context(), purchaseIntentState{
			MSISDN:       acc.MSISDN,
			OptionCode:   optionCode,
			Method:       method,
			WalletNumber: acc.MSISDN,
		})
		if err != nil {
			return ctx.Answer("Gagal memuat detail paket terbaru. Silakan coba lagi.", true)
		}
		state.Draft = &intent
		screen, err := p.menuMgr.BuildCheckoutScreen(quote)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal membuat sesi checkout: %v", err), true)
		}
		return p.assistantTransitionWithTTL(ctx, state, assistantConfirmationTTL, screen)

	case "custom_price":
		state.Wizard = "custom_price"
		state.OptionCode = p.menuMgr.ResolveOptionCode(opaque)
		state.Method = "balance"
		prompt := fmt.Sprintf(
			"✏️ <b>Set Harga Kustom (Overwrite)</b>\n\nPaket: <code>%s</code>\n\nKirimkan nominal harga dalam Rupiah (contoh: <code>0</code> atau <code>1000</code>):\n\n<i>Ketik <code>/cancel</code> untuk membatalkan.</i>",
			html.EscapeString(state.OptionCode),
		)
		if err := p.assistantAwait(ctx, state, prompt); err != nil {
			return err
		}
		return ctx.Answer("Masukkan harga kustom…", false)

	case "checkout", "buy_confirm":
		if state.Draft == nil {
			return ctx.Answer("Draft pembelian tidak valid atau sudah kedaluwarsa", true)
		}
		if _, err := normalizePurchaseIntent(*state.Draft); err != nil {
			return ctx.Answer("Draft pembelian tidak valid atau sudah kedaluwarsa", true)
		}
		return p.confirmAssistantPurchase(ctx, state, *state.Draft)

	case "cancel_draft", "buy_cancel":
		state.Draft = nil
		_ = ctx.Answer("Pembelian dibatalkan", false)
		screen, err := p.menuMgr.BuildStoreScreen(ctx.Context())
		if err != nil {
			return err
		}
		return p.assistantTransition(ctx, state, screen)

	case "pending_qris":
		screen, err := p.menuMgr.BuildPendingQRISScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat QRIS: %v", err), true)
		}
		return p.assistantTransition(ctx, state, screen)

	case "qris_cancel":
		if opaque == "" || opaque == "noop" {
			return ctx.Answer("Kode transaksi QRIS tidak valid", true)
		}
		raw, view, err := assistantDestructiveConfirmation(
			state,
			"⚠️ <b>Batalkan Transaksi QRIS</b>\n\nTransaksi pending akan dihapus dari penyimpanan bot. Lanjutkan?",
			fmt.Sprintf("myxl:qris_cancel_exec:%s", opaque),
			"myxl:pending_qris",
			"🗑️ Ya, Batalkan",
		)
		if err != nil {
			return err
		}
		return ctx.Transition(raw, assistantConfirmationTTL, view)

	case "qris_cancel_exec":
		if opaque == "" || opaque == "noop" {
			return ctx.Answer("Kode transaksi QRIS tidak valid", true)
		}
		if err := p.repo.DeletePendingQRIS(ctx.Context(), opaque); err != nil {
			return ctx.Answer("Gagal membatalkan transaksi QRIS. Silakan coba lagi.", true)
		}
		_ = ctx.Answer("✅ Transaksi QRIS dibatalkan", false)
		screen, err := p.menuMgr.BuildDashboardScreen(ctx.Context(), false)
		if err != nil {
			return err
		}
		return p.assistantTransition(ctx, state, screen)

	case "qris_img":
		qrCode := p.menuMgr.ResolveQR(opaque)
		if qrCode == "" || qrCode == opaque {
			if acc, _ := p.repo.GetActive(ctx.Context()); acc != nil {
				if pending, _ := p.repo.GetPendingQRIS(ctx.Context(), acc.MSISDN); pending != nil {
					qrCode = pending.QRCode
				}
			}
		}
		if qrCode == "" {
			return ctx.Answer("Kode QRIS tidak ditemukan atau sudah kedaluwarsa", true)
		}
		rt := p.currentAssistantRuntime()
		target, ok := ctx.Target().(presentationtelegram.MessageTarget)
		if !ok || target.Peer == nil || rt.Service == nil {
			return ctx.Answer("Layanan pengiriman foto tidak tersedia", true)
		}
		if err := p.sendQRPhoto(ctx.Context(), rt.Service, target.Peer, qrCode, "QRIS MyXL", 0); err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal mengirim foto QRIS: %v", err), true)
		}
		return ctx.Answer("✅ Foto QRIS berhasil dikirim!", false)

	case "bookmark_add":
		optionCode := p.menuMgr.ResolveOptionCode(opaque)
		acc, err := p.repo.GetActive(ctx.Context())
		if err != nil || acc == nil {
			return ctx.Answer("Tidak ada akun aktif", true)
		}
		details, err := p.client.GetPackageDetails(ctx.Context(), acc, optionCode)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal membaca paket: %v", err), true)
		}
		pkgName := optionCode
		var price int64
		if details.PackageOption != nil {
			pkgName = details.PackageOption.Name
			price = int64(details.PackageOption.Price)
		}
		if err := p.repo.SavePackage(ctx.Context(), &SavedPackage{
			MSISDN: acc.MSISDN, OptionCode: optionCode, Name: pkgName, Price: price,
		}); err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal menyimpan favorit: %v", err), true)
		}
		return ctx.Answer("⭐ Paket berhasil disimpan ke favorit!", true)

	case "bookmark_del":
		optionCode := p.menuMgr.ResolveOptionCode(opaque)
		if optionCode == "" {
			return ctx.Answer("Kode paket favorit tidak valid", true)
		}
		raw, view, err := assistantDestructiveConfirmation(
			state,
			fmt.Sprintf("⚠️ <b>Hapus Paket Favorit</b>\n\nHapus paket <code>%s</code> dari favorit?", html.EscapeString(optionCode)),
			fmt.Sprintf("myxl:bookmark_del_exec:%s", opaque),
			"myxl:saved",
			"🗑️ Ya, Hapus",
		)
		if err != nil {
			return err
		}
		return ctx.Transition(raw, assistantConfirmationTTL, view)

	case "bookmark_del_exec":
		optionCode := p.menuMgr.ResolveOptionCode(opaque)
		acc, _ := p.repo.GetActive(ctx.Context())
		if acc != nil {
			if err := p.repo.DeleteSavedPackage(ctx.Context(), acc.MSISDN, optionCode); err != nil {
				return ctx.Answer(fmt.Sprintf("Gagal menghapus favorit: %v", err), true)
			}
		}
		_ = ctx.Answer("Paket dihapus dari favorit", false)
		screen, err := p.menuMgr.BuildSavedPackagesScreen(ctx.Context())
		if err != nil {
			return err
		}
		return p.assistantTransition(ctx, state, screen)

	case "noop":
		return ctx.Answer("", false)

	default:
		return ctx.Answer("Tombol tidak dikenali atau sudah kedaluwarsa", true)
	}
}

func (p *Plugin) HandleAssistantInput(ctx *orchestration.Context, text string) error {
	if p == nil || ctx == nil {
		return orchestration.ErrInvalidEngine
	}
	rt := p.currentAssistantRuntime()
	session := ctx.Session()
	if rt.Admit == nil {
		return fmt.Errorf("myxl: assistant runtime unavailable")
	}
	if err := rt.Admit(p.Name(), feature.InteractionScreen, assistantScreenInput, session.Binding.ActorID, ctx.Target()); err != nil {
		return err
	}

	state := decodeAssistantState(ctx.State())
	input := strings.TrimSpace(text)
	if strings.EqualFold(input, "/cancel") {
		screen, err := p.menuMgr.BuildDashboardScreen(ctx.Context(), false)
		if err != nil {
			return err
		}
		return p.assistantTransition(ctx, assistantState{}, screen)
	}
	if strings.HasPrefix(input, "/") {
		return p.assistantRearm(ctx, state, "⚠️ Selesaikan input MyXL ini atau ketik <code>/cancel</code>.")
	}

	switch state.Wizard {
	case "login_msisdn":
		return p.assistantInputMSISDN(ctx, state, input)
	case "login_otp":
		return p.assistantInputOTP(ctx, state, input)
	case "alias":
		return p.assistantInputAlias(ctx, state, input)
	case "option_code":
		return p.assistantInputOptionCode(ctx, state, input)
	case "custom_price":
		return p.assistantInputCustomPrice(ctx, state, input)
	case "family":
		return p.assistantInputFamily(ctx, state, input)
	default:
		return fmt.Errorf("myxl: no assistant input wizard is active")
	}
}

func (p *Plugin) assistantRearm(ctx *orchestration.Context, state assistantState, message string) error {
	prompt := message + "\n\n<i>Ketik <code>/cancel</code> untuk membatalkan.</i>"
	return p.assistantAwait(ctx, state, prompt)
}

func (p *Plugin) assistantInputMSISDN(ctx *orchestration.Context, state assistantState, input string) error {
	msisdn, err := NormalizeMSISDN(input)
	if err != nil {
		return p.assistantRearm(ctx, state, fmt.Sprintf("⚠️ <b>Nomor HP tidak valid:</b> %s\nContoh: <code>081912345678</code> atau <code>6281912345678</code>.", html.EscapeString(err.Error())))
	}
	cCtx, cancel := context.WithTimeout(ctx.Context(), 25*time.Second)
	defer cancel()
	subID, err := p.client.RequestOTP(cCtx, msisdn)
	if err != nil {
		return p.assistantRearm(ctx, state, fmt.Sprintf("❌ <b>Gagal meminta OTP dari MyXL:</b>\n<code>%s</code>", html.EscapeString(err.Error())))
	}
	existing, _ := p.repo.GetByMSISDN(cCtx, msisdn)
	if existing == nil {
		existing = &Account{MSISDN: msisdn, SubscriberID: subID}
	} else if subID != "" {
		existing.SubscriberID = subID
	}
	if err := p.repo.Save(cCtx, existing); err != nil {
		return p.assistantRearm(ctx, state, fmt.Sprintf("❌ Gagal menyimpan sesi login: <code>%s</code>", html.EscapeString(err.Error())))
	}
	state.Wizard = "login_otp"
	state.MSISDN = msisdn
	prompt := fmt.Sprintf(
		"📩 <b>Login MyXL — Langkah 2 dari 2</b>\n\nKode OTP 6-digit telah dikirim via SMS ke <code>%s</code>.\n\nSilakan kirimkan <b>6 digit kode OTP</b> Anda sekarang.\n\n<i>Ketik <code>/cancel</code> untuk membatalkan.</i>",
		html.EscapeString(msisdn),
	)
	return p.assistantAwait(ctx, state, prompt)
}

func (p *Plugin) assistantInputOTP(ctx *orchestration.Context, state assistantState, input string) error {
	code, err := normalizeOTPCode(input)
	if err != nil {
		return p.assistantRearm(ctx, state, "⚠️ <b>Kode OTP harus berupa 6 digit angka.</b>")
	}
	cCtx, cancel := context.WithTimeout(ctx.Context(), 25*time.Second)
	defer cancel()
	tokens, err := p.client.SubmitOTP(cCtx, state.MSISDN, code)
	if err != nil {
		return p.assistantRearm(ctx, state, fmt.Sprintf("❌ <b>Verifikasi OTP gagal:</b>\n<code>%s</code>", html.EscapeString(err.Error())))
	}
	acc, _ := p.repo.GetByMSISDN(cCtx, state.MSISDN)
	if acc == nil {
		acc = &Account{MSISDN: state.MSISDN}
	}
	acc.AccessToken = tokens.AccessToken
	acc.IDToken = tokens.IDToken
	acc.RefreshToken = tokens.RefreshToken
	if tokens.ExpiresIn > 0 {
		acc.TokenExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	} else {
		acc.TokenExpiresAt = time.Now().Add(DefaultTokenExpiryFallback)
	}
	acc.IsActive = true
	if err := p.repo.Save(cCtx, acc); err != nil {
		return p.assistantRearm(ctx, state, fmt.Sprintf("⚠️ Login berhasil di CIAM tetapi gagal disimpan: <code>%s</code>", html.EscapeString(err.Error())))
	}
	screen, err := p.menuMgr.BuildDashboardScreen(cCtx, false)
	if err != nil {
		return err
	}
	return p.assistantTransition(ctx, assistantState{}, screen)
}

func (p *Plugin) assistantInputAlias(ctx *orchestration.Context, state assistantState, input string) error {
	alias, err := normalizeAlias(input)
	if err != nil {
		return p.assistantRearm(ctx, state, "⚠️ "+html.EscapeString(err.Error())+".")
	}
	if err := p.repo.SetAlias(ctx.Context(), state.MSISDN, alias); err != nil {
		return p.assistantRearm(ctx, state, fmt.Sprintf("❌ Gagal menyimpan alias: %v", err))
	}
	screen, err := p.menuMgr.BuildAccountsScreen(ctx.Context())
	if err != nil {
		return err
	}
	return p.assistantTransition(ctx, assistantState{}, screen)
}

func (p *Plugin) assistantInputOptionCode(ctx *orchestration.Context, state assistantState, input string) error {
	optionCode := strings.TrimSpace(input)
	if optionCode == "" {
		return p.assistantRearm(ctx, state, "⚠️ Kode paket tidak boleh kosong.")
	}
	acc, err := p.repo.GetActive(ctx.Context())
	if err != nil || acc == nil {
		return p.assistantRearm(ctx, state, "❌ Tidak ada akun MyXL aktif.")
	}
	screen, err := p.menuMgr.BuildPackageDetailScreen(ctx.Context(), acc, optionCode)
	if err != nil {
		return p.assistantRearm(ctx, state, fmt.Sprintf("❌ Gagal memuat paket <code>%s</code>: <code>%s</code>", html.EscapeString(optionCode), html.EscapeString(err.Error())))
	}
	return p.assistantTransition(ctx, assistantState{}, screen)
}

func (p *Plugin) assistantInputCustomPrice(ctx *orchestration.Context, state assistantState, input string) error {
	value, err := strconv.ParseInt(strings.TrimSpace(input), 10, 64)
	if err != nil || value < 0 {
		return p.assistantRearm(ctx, state, "⚠️ Nominal harga tidak valid. Masukkan angka bulat non-negatif.")
	}
	acc, err := p.repo.GetActive(ctx.Context())
	if err != nil || acc == nil {
		return p.assistantRearm(ctx, state, "❌ Tidak ada akun MyXL aktif.")
	}
	intent, quote, err := p.preparePurchaseIntent(ctx.Context(), purchaseIntentState{
		MSISDN:         acc.MSISDN,
		OptionCode:     state.OptionCode,
		Method:         state.Method,
		WalletNumber:   acc.MSISDN,
		OverwritePrice: value,
		HasOverwrite:   true,
	})
	if err != nil {
		return p.assistantRearm(ctx, state, "❌ Gagal memuat detail paket terbaru.")
	}
	state.Wizard = ""
	state.Draft = &intent
	screen, err := p.menuMgr.BuildCheckoutScreen(quote)
	if err != nil {
		return err
	}
	return p.assistantTransition(ctx, state, screen)
}

func (p *Plugin) assistantInputFamily(ctx *orchestration.Context, state assistantState, input string) error {
	familyCode := strings.TrimSpace(input)
	if familyCode == "" {
		return p.assistantRearm(ctx, state, "⚠️ Family Code tidak boleh kosong.")
	}
	acc, err := p.repo.GetActive(ctx.Context())
	if err != nil || acc == nil {
		return p.assistantRearm(ctx, state, "❌ Tidak ada akun aktif terhubung.")
	}
	screen, err := p.menuMgr.BuildFamilyPackagesScreen(ctx.Context(), acc, familyCode, 1)
	if err != nil {
		return p.assistantRearm(ctx, state, fmt.Sprintf("⚠️ <b>Gagal mencari paket:</b> %v", err))
	}
	return p.assistantTransition(ctx, assistantState{}, screen)
}

func (p *Plugin) confirmAssistantPurchase(ctx *orchestration.Context, state assistantState, intent purchaseIntentState) error {
	cCtx, cancel := context.WithTimeout(ctx.Context(), 45*time.Second)
	defer cancel()

	resolved, err := p.resolvePurchaseIntent(cCtx, intent)
	if err != nil {
		if errors.Is(err, ErrPurchaseQuoteChanged) {
			return ctx.Answer("Harga paket berubah sejak halaman konfirmasi dibuat. Pembelian tidak dijalankan; buka ulang paket untuk harga terbaru.", true)
		}
		return ctx.Answer("Detail pembelian tidak lagi valid. Buka ulang paket lalu coba lagi.", true)
	}

	reserved, err := p.repo.ReservePurchase(
		cCtx,
		resolved.IdempotencyKey,
		resolved.Intent.MSISDN,
		resolved.Intent.OptionCode,
		resolved.Intent.Method,
	)
	if err != nil {
		return ctx.Answer("Gagal mengamankan transaksi. Pembelian tidak dijalankan.", true)
	}
	if !reserved {
		return ctx.Answer("Transaksi sedang diproses atau baru saja dikonfirmasi.", true)
	}

	var result *SettlementResult
	switch resolved.Intent.Method {
	case "balance":
		result, err = p.client.SettlementBalance(cCtx, resolved.Account, resolved.Item, resolved.Overwrite)
	case "qris":
		result, err = p.client.SettlementQRIS(cCtx, resolved.Account, resolved.Item, resolved.Overwrite)
	case "gopay", "ovo", "dana", "shopeepay":
		result, err = p.client.SettlementMultipayment(
			cCtx,
			resolved.Account,
			resolved.Item,
			strings.ToUpper(resolved.Intent.Method),
			resolved.Intent.WalletNumber,
			resolved.Overwrite,
		)
	case "decoy_balance":
		result, err = p.client.SettlementDecoy(cCtx, resolved.Account, resolved.Item, "balance", resolved.Overwrite)
	case "decoy_qris":
		result, err = p.client.SettlementDecoy(cCtx, resolved.Account, resolved.Item, "qris", resolved.Overwrite)
	case "decoy_qris0":
		result, err = p.client.SettlementDecoy(cCtx, resolved.Account, resolved.Item, "qris0", resolved.Overwrite)
	default:
		err = fmt.Errorf("unsupported payment method %q", resolved.Intent.Method)
	}
	if err != nil {
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
		_ = p.repo.FinishPurchase(persistCtx, resolved.IdempotencyKey, "UNKNOWN", "", err.Error())
		persistCancel()
		return ctx.Answer("Hasil transaksi tidak dapat dipastikan. Periksa riwayat MyXL sebelum mencoba lagi.", true)
	}
	if result == nil {
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
		_ = p.repo.FinishPurchase(persistCtx, resolved.IdempotencyKey, "UNKNOWN", "", "empty settlement result")
		persistCancel()
		return ctx.Answer("Hasil transaksi kosong dan tidak dapat dipastikan.", true)
	}

	status := "FAILED"
	if result.IsSuccess {
		status = "SUCCESS"
	}
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
	finishErr := p.repo.FinishPurchase(persistCtx, resolved.IdempotencyKey, status, result.TransactionCode, result.Message)
	persistCancel()
	if finishErr != nil {
		return ctx.Answer("Transaksi selesai tetapi hasilnya gagal dicatat. Periksa riwayat MyXL.", true)
	}

	var qrWarning string
	if result.QRCode != "" {
		qrPayload, qrErr := normalizeQRPayload(result.QRCode)
		if qrErr != nil {
			qrWarning = "Payload QRIS dari operator tidak valid; gambar QR tidak dibuat."
			result.QRCode = ""
		} else {
			result.QRCode = qrPayload
			now := time.Now().UTC()
			pending := &PendingQRIS{
				TransactionCode: result.TransactionCode,
				IdempotencyKey:  resolved.IdempotencyKey,
				MSISDN:          resolved.Intent.MSISDN,
				OptionCode:      resolved.Intent.OptionCode,
				PackageName:     resolved.PackageName,
				Price:           resolved.EffectivePrice,
				QRCode:          qrPayload,
				Status:          "PENDING",
				CreatedAt:       now,
				ExpiresAt:       now.Add(pendingQRISTTL),
			}
			if err := p.repo.SavePendingQRIS(cCtx, pending); err != nil {
				qrWarning = "QRIS berhasil dibuat tetapi gagal disimpan untuk dilihat kembali."
			}
		}
	}

	state.Draft = nil
	screen := p.menuMgr.BuildPurchaseResultScreen(
		result,
		resolved.PackageName,
		resolved.EffectivePrice,
		resolved.Intent.Method,
		resolved.Intent.OptionCode,
	)
	if err := p.assistantTransition(ctx, state, screen); err != nil {
		return err
	}

	if result.QRCode != "" {
		rt := p.currentAssistantRuntime()
		if target, ok := ctx.Target().(presentationtelegram.MessageTarget); ok && target.Peer != nil && rt.Service != nil {
			if err := p.sendQRPhoto(ctx.Context(), rt.Service, target.Peer, result.QRCode, resolved.PackageName, resolved.EffectivePrice); err != nil && qrWarning == "" {
				qrWarning = "Transaksi selesai tetapi foto QRIS gagal dikirim."
			}
		}
	}
	if qrWarning != "" {
		return ctx.Answer(qrWarning, true)
	}
	if result.IsSuccess {
		return ctx.Answer("✅ Pembelian selesai", false)
	}
	return ctx.Answer("Pembelian selesai dengan status gagal.", true)
}

var _ assistantinteraction.FeatureDriver = (*Plugin)(nil)
var _ interface{ FeatureSpec() feature.Spec } = (*Plugin)(nil)
