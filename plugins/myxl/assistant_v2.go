package myxl

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	assistantV2TTL       = 10 * time.Minute
	assistantV2InputTTL  = 2 * time.Minute
	assistantV2SlotCount = 32

	assistantV2ScreenHome  = "home"
	assistantV2ScreenInput = "input"
)

type assistantV2State struct {
	Slots      []string            `json:"slots,omitempty"`
	Wizard     string              `json:"wizard,omitempty"`
	MSISDN     string              `json:"msisdn,omitempty"`
	OptionCode string              `json:"option_code,omitempty"`
	Method     string              `json:"method,omitempty"`
	Draft      *purchaseDraftState `json:"draft,omitempty"`
}

func (p *Plugin) AssistantFeatureID() string { return p.Name() }

func (p *Plugin) FeatureSpec() feature.Spec {
	policy := feature.OwnerPolicy(execution.SurfaceAssistant)
	policy.PrivateOnly = true
	interactions := []feature.Interaction{
		{
			ID:          assistantV2ScreenHome,
			Kind:        feature.InteractionScreen,
			Description: "MyXL interactive dashboard",
			Surfaces:    execution.SurfaceAssistant,
			Policy:      policy,
		},
		{
			ID:          assistantV2ScreenInput,
			Kind:        feature.InteractionScreen,
			Description: "MyXL bounded free-form input",
			Surfaces:    execution.SurfaceAssistant,
			Policy:      policy,
		},
	}
	for i := 0; i < assistantV2SlotCount; i++ {
		interactions = append(interactions, feature.Interaction{
			ID:          assistantV2SlotID(i),
			Kind:        feature.InteractionAction,
			Description: "MyXL session-bound action slot",
			Surfaces:    execution.SurfaceAssistant,
			Policy:      policy,
		})
	}
	return feature.Spec{
		ID:           p.Name(),
		Name:         "MyXL",
		Description:  p.Description(),
		Category:     "Utility",
		Interactions: interactions,
	}
}

func assistantV2SlotID(index int) string {
	return fmt.Sprintf("slot_%02d", index)
}

func (p *Plugin) BindAssistantV2(rt assistantinteraction.V2Runtime) (func(), error) {
	if p == nil || rt.Engine == nil || rt.Catalog == nil || rt.Admit == nil {
		return nil, orchestration.ErrInvalidEngine
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope.IsZero() {
		return nil, fmt.Errorf("myxl: assistant a2 feature scope unavailable")
	}

	registrations := make([]interface{ Close() }, 0, assistantV2SlotCount)
	for i := 0; i < assistantV2SlotCount; i++ {
		slot := i
		actionID := assistantV2SlotID(slot)
		reg, err := rt.Engine.RegisterAction(scope, p.Name(), actionID, func(ctx *orchestration.Context) error {
			if ctx == nil {
				return orchestration.ErrInvalidEngine
			}
			session := ctx.Session()
			if err := rt.Admit(p.Name(), feature.InteractionAction, actionID, session.Binding.ActorID, ctx.Target()); err != nil {
				return err
			}
			return p.handleAssistantV2Slot(ctx, slot)
		})
		if err != nil {
			for j := len(registrations) - 1; j >= 0; j-- {
				registrations[j].Close()
			}
			return nil, fmt.Errorf("myxl: register assistant a2 %s: %w", actionID, err)
		}
		registrations = append(registrations, reg)
	}

	p.assistantMu.Lock()
	p.assistantV2 = rt
	p.assistantMu.Unlock()

	cleanup := func() {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
		p.assistantMu.Lock()
		if p.assistantV2.Engine == rt.Engine {
			p.assistantV2 = assistantinteraction.V2Runtime{}
		}
		p.assistantMu.Unlock()
	}
	return cleanup, nil
}

func (p *Plugin) assistantRuntime() assistantinteraction.V2Runtime {
	if p == nil {
		return assistantinteraction.V2Runtime{}
	}
	p.assistantMu.RLock()
	rt := p.assistantV2
	p.assistantMu.RUnlock()
	return rt
}

func (p *Plugin) openAssistantV2(cmd *core.Context) error {
	if p == nil || cmd == nil || cmd.PeerID == nil || cmd.SenderID() == 0 {
		return fmt.Errorf("myxl: assistant a2 command target unavailable")
	}
	rt := p.assistantRuntime()
	if rt.Engine == nil || rt.Admit == nil {
		return fmt.Errorf("myxl: assistant a2 runtime unavailable")
	}
	target := presentationtelegram.MessageTarget{Peer: cmd.PeerID, ChatID: cmd.ChatID()}
	if err := rt.Admit(p.Name(), feature.InteractionScreen, assistantV2ScreenHome, cmd.SenderID(), target); err != nil {
		return err
	}
	screen, err := p.menuMgr.BuildDashboardScreen(cmd.Ctx, false)
	if err != nil {
		return err
	}
	state, view, err := p.assistantV2Screen(assistantV2State{}, screen)
	if err != nil {
		return err
	}
	_, err = rt.Engine.Begin(cmd.Ctx, orchestration.BeginRequest{
		FeatureID: p.Name(),
		ActorID:   cmd.SenderID(),
		State:     state,
		TTL:       assistantV2TTL,
		Target:    target,
		View:      view,
	})
	return err
}

func decodeAssistantV2State(raw []byte) assistantV2State {
	var state assistantV2State
	if len(raw) == 0 {
		return state
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return assistantV2State{}
	}
	return state
}

func encodeAssistantV2State(state assistantV2State) ([]byte, error) {
	return json.Marshal(state)
}

func (p *Plugin) assistantV2Screen(state assistantV2State, screen *ui.Screen) ([]byte, presentation.View, error) {
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
			if slot >= assistantV2SlotCount {
				return nil, presentation.View{}, fmt.Errorf("myxl: assistant screen exceeds %d action slots", assistantV2SlotCount)
			}
			state.Slots = append(state.Slots, string(button.Data))
			out = append(out, presentation.Button{
				Text:     button.Text,
				ActionID: assistantV2SlotID(slot),
			})
			slot++
		}
		if len(out) > 0 {
			rows = append(rows, out)
		}
	}
	raw, err := encodeAssistantV2State(state)
	if err != nil {
		return nil, presentation.View{}, err
	}
	view := presentation.View{Text: screen.Text(), Rows: rows}
	if err := view.Validate(); err != nil {
		return nil, presentation.View{}, err
	}
	return raw, view, nil
}

func (p *Plugin) assistantV2Transition(ctx *orchestration.Context, state assistantV2State, screen *ui.Screen) error {
	state.Wizard = ""
	raw, view, err := p.assistantV2Screen(state, screen)
	if err != nil {
		return err
	}
	return ctx.Transition(raw, assistantV2TTL, view)
}

func (p *Plugin) assistantV2Await(ctx *orchestration.Context, state assistantV2State, prompt string) error {
	state.Slots = []string{"myxl:cancel_wizard"}
	raw, err := encodeAssistantV2State(state)
	if err != nil {
		return err
	}
	view := presentation.View{
		Text: prompt,
		Rows: []presentation.Row{{
			{Text: "❌ Batal", ActionID: assistantV2SlotID(0)},
		}},
	}
	return ctx.AwaitInput(raw, assistantV2InputTTL, view)
}

func parseAssistantV2Template(data string) (namespace, action, opaque string, err error) {
	parts := strings.SplitN(strings.TrimSpace(data), ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
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

func (p *Plugin) handleAssistantV2Slot(ctx *orchestration.Context, slot int) error {
	state := decodeAssistantV2State(ctx.State())
	if slot < 0 || slot >= len(state.Slots) {
		return ctx.Answer("Interaction expired. Reopen MyXL.", true)
	}
	namespace, action, opaque, err := parseAssistantV2Template(state.Slots[slot])
	if err != nil {
		return ctx.Answer("Interaction data is invalid. Reopen MyXL.", true)
	}
	if namespace == "assistant" && action == "close" {
		if err := ctx.Edit(presentation.View{Text: "✅ Menu MyXL ditutup."}); err != nil {
			return err
		}
		ctx.Cancel()
		return ctx.Answer("", false)
	}
	if namespace != p.Name() {
		return ctx.Answer("Interaction owner mismatch. Reopen MyXL.", true)
	}
	return p.dispatchAssistantV2Action(ctx, state, action, opaque)
}

func (p *Plugin) dispatchAssistantV2Action(ctx *orchestration.Context, state assistantV2State, action, opaque string) error {
	switch action {
	case "home", "refresh":
		screen, err := p.menuMgr.BuildDashboardScreen(ctx.Context(), false)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat MyXL: %v", err), true)
		}
		if action == "refresh" {
			_ = ctx.Answer("🔄 Kuota & pulsa diperbarui", false)
		}
		return p.assistantV2Transition(ctx, state, screen)

	case "detail", "quota":
		screen, err := p.menuMgr.BuildQuotaDetailScreen(ctx.Context(), false)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat rincian: %v", err), true)
		}
		return p.assistantV2Transition(ctx, state, screen)

	case "accounts":
		screen, err := p.menuMgr.BuildAccountsScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat akun: %v", err), true)
		}
		return p.assistantV2Transition(ctx, state, screen)

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
		return p.assistantV2Transition(ctx, state, screen)

	case "alias_pick":
		screen, err := p.menuMgr.BuildAliasPickScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat akun: %v", err), true)
		}
		return p.assistantV2Transition(ctx, state, screen)

	case "alias_req":
		state.Wizard = "alias"
		state.MSISDN = opaque
		prompt := fmt.Sprintf(
			"🏷️ <b>Ubah Alias Akun</b>\n\nNomor: <code>%s</code>\n\nSilakan kirimkan nama alias baru (maksimal 24 karakter):\n\n<i>Ketik <code>/cancel</code> untuk membatalkan.</i>",
			html.EscapeString(opaque),
		)
		if err := p.assistantV2Await(ctx, state, prompt); err != nil {
			return err
		}
		return ctx.Answer("Ketik nama alias baru…", false)

	case "del_pick":
		screen, err := p.menuMgr.BuildDeletePickScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat akun: %v", err), true)
		}
		return p.assistantV2Transition(ctx, state, screen)

	case "del_ask":
		state.Slots = []string{
			fmt.Sprintf("myxl:del_exec:%s", opaque),
			"myxl:accounts",
		}
		raw, err := encodeAssistantV2State(state)
		if err != nil {
			return err
		}
		view := presentation.View{
			Text: fmt.Sprintf("⚠️ <b>Hapus Akun MyXL</b>\n\nApakah Anda yakin ingin menghapus nomor <code>%s</code> dari penyimpanan bot?", html.EscapeString(opaque)),
			Rows: []presentation.Row{{
				{Text: "🗑️ Ya, Hapus", ActionID: assistantV2SlotID(0)},
				{Text: "❌ Batal", ActionID: assistantV2SlotID(1)},
			}},
		}
		return ctx.Transition(raw, assistantV2TTL, view)

	case "del_exec":
		if err := p.repo.Delete(ctx.Context(), opaque); err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal menghapus: %v", err), true)
		}
		_ = ctx.Answer("✅ Akun berhasil dihapus", false)
		screen, err := p.menuMgr.BuildAccountsScreen(ctx.Context())
		if err != nil {
			return err
		}
		return p.assistantV2Transition(ctx, state, screen)

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
		return p.assistantV2Transition(ctx, state, screen)

	case "login_req":
		state.Wizard = "login_msisdn"
		state.MSISDN = ""
		prompt := "📱 <b>Login MyXL — Langkah 1 dari 2</b>\n\n" +
			"Masukkan nomor HP XL/Axis yang ingin didaftarkan.\n" +
			"Format: <code>0819...</code> atau <code>62819...</code>\n\n" +
			"<i>Ketik <code>/cancel</code> atau tekan Batal di bawah untuk membatalkan.</i>"
		if err := p.assistantV2Await(ctx, state, prompt); err != nil {
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
		return p.assistantV2Transition(ctx, assistantV2State{}, screen)

	case "store":
		screen, err := p.menuMgr.BuildStoreScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat store: %v", err), true)
		}
		return p.assistantV2Transition(ctx, state, screen)

	case "saved":
		screen, err := p.menuMgr.BuildSavedPackagesScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat favorit: %v", err), true)
		}
		return p.assistantV2Transition(ctx, state, screen)

	case "fam_input":
		state.Wizard = "family"
		prompt := "🔍 <b>Input Family Code Paket</b>\n\n" +
			"Silakan kirimkan Family Code paket yang ingin Anda telusuri (contoh: <code>7658c955-a0b9-405f-bb17-de7f43d1a946</code>):\n\n" +
			"<i>Ketik <code>/cancel</code> untuk membatalkan.</i>"
		if err := p.assistantV2Await(ctx, state, prompt); err != nil {
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
		return p.assistantV2Transition(ctx, state, screen)

	case "buy_opt_input":
		state.Wizard = "option_code"
		prompt := "⚡ <b>Input Option Code Paket</b>\n\n" +
			"Silakan kirimkan kode paket yang ingin Anda beli (contoh: <code>OPT12345</code>):\n\n" +
			"<i>Ketik <code>/cancel</code> untuk membatalkan.</i>"
		if err := p.assistantV2Await(ctx, state, prompt); err != nil {
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
		return p.assistantV2Transition(ctx, state, screen)

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
		details, err := p.client.GetPackageDetails(ctx.Context(), acc, optionCode)
		if err != nil || details.TokenConfirmation == "" {
			return ctx.Answer("Gagal memuat token konfirmasi", true)
		}
		pkgName := optionCode
		var price int64
		if details.PackageOption != nil {
			pkgName = details.PackageOption.Name
			price = int64(details.PackageOption.Price)
		}
		draft := purchaseDraftState{
			MSISDN: acc.MSISDN, OptionCode: optionCode, PackageName: pkgName, Price: price,
			TokenConfirmation: details.TokenConfirmation, Method: method, WalletNumber: acc.MSISDN,
		}
		state.Draft = &draft
		screen, err := p.menuMgr.BuildCheckoutScreen(draft)
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal membuat sesi checkout: %v", err), true)
		}
		return p.assistantV2Transition(ctx, state, screen)

	case "custom_price":
		state.Wizard = "custom_price"
		state.OptionCode = p.menuMgr.ResolveOptionCode(opaque)
		state.Method = "balance"
		prompt := fmt.Sprintf(
			"✏️ <b>Set Harga Kustom (Overwrite)</b>\n\nPaket: <code>%s</code>\n\nKirimkan nominal harga dalam Rupiah (contoh: <code>0</code> atau <code>1000</code>):\n\n<i>Ketik <code>/cancel</code> untuk membatalkan.</i>",
			html.EscapeString(state.OptionCode),
		)
		if err := p.assistantV2Await(ctx, state, prompt); err != nil {
			return err
		}
		return ctx.Answer("Masukkan harga kustom…", false)

	case "checkout", "buy_confirm":
		if state.Draft == nil || state.Draft.MSISDN == "" || state.Draft.OptionCode == "" || state.Draft.TokenConfirmation == "" {
			return ctx.Answer("Draft pembelian tidak valid atau sudah kedaluwarsa", true)
		}
		return p.confirmAssistantV2Purchase(ctx, state, *state.Draft)

	case "cancel_draft", "buy_cancel":
		state.Draft = nil
		_ = ctx.Answer("Pembelian dibatalkan", false)
		screen, err := p.menuMgr.BuildStoreScreen(ctx.Context())
		if err != nil {
			return err
		}
		return p.assistantV2Transition(ctx, state, screen)

	case "pending_qris":
		screen, err := p.menuMgr.BuildPendingQRISScreen(ctx.Context())
		if err != nil {
			return ctx.Answer(fmt.Sprintf("Gagal memuat QRIS: %v", err), true)
		}
		return p.assistantV2Transition(ctx, state, screen)

	case "qris_cancel":
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
		return p.assistantV2Transition(ctx, state, screen)

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
		rt := p.assistantRuntime()
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
		return p.assistantV2Transition(ctx, state, screen)

	case "noop":
		return ctx.Answer("", false)

	default:
		return ctx.Answer("Tombol tidak dikenali atau sudah kedaluwarsa", true)
	}
}

func (p *Plugin) HandleAssistantV2Input(ctx *orchestration.Context, text string) error {
	if p == nil || ctx == nil {
		return orchestration.ErrInvalidEngine
	}
	rt := p.assistantRuntime()
	session := ctx.Session()
	if rt.Admit == nil {
		return fmt.Errorf("myxl: assistant a2 runtime unavailable")
	}
	if err := rt.Admit(p.Name(), feature.InteractionScreen, assistantV2ScreenInput, session.Binding.ActorID, ctx.Target()); err != nil {
		return err
	}

	state := decodeAssistantV2State(ctx.State())
	input := strings.TrimSpace(text)
	if strings.EqualFold(input, "/cancel") {
		screen, err := p.menuMgr.BuildDashboardScreen(ctx.Context(), false)
		if err != nil {
			return err
		}
		return p.assistantV2Transition(ctx, assistantV2State{}, screen)
	}
	if strings.HasPrefix(input, "/") {
		return p.assistantV2Rearm(ctx, state, "⚠️ Selesaikan input MyXL ini atau ketik <code>/cancel</code>.")
	}

	switch state.Wizard {
	case "login_msisdn":
		return p.assistantV2InputMSISDN(ctx, state, input)
	case "login_otp":
		return p.assistantV2InputOTP(ctx, state, input)
	case "alias":
		return p.assistantV2InputAlias(ctx, state, input)
	case "option_code":
		return p.assistantV2InputOptionCode(ctx, state, input)
	case "custom_price":
		return p.assistantV2InputCustomPrice(ctx, state, input)
	case "family":
		return p.assistantV2InputFamily(ctx, state, input)
	default:
		return fmt.Errorf("myxl: no assistant a2 input wizard is active")
	}
}

func (p *Plugin) assistantV2Rearm(ctx *orchestration.Context, state assistantV2State, message string) error {
	prompt := message + "\n\n<i>Ketik <code>/cancel</code> untuk membatalkan.</i>"
	return p.assistantV2Await(ctx, state, prompt)
}

func (p *Plugin) assistantV2InputMSISDN(ctx *orchestration.Context, state assistantV2State, input string) error {
	msisdn, err := NormalizeMSISDN(input)
	if err != nil {
		return p.assistantV2Rearm(ctx, state, fmt.Sprintf("⚠️ <b>Nomor HP tidak valid:</b> %s\nContoh: <code>081912345678</code> atau <code>6281912345678</code>.", html.EscapeString(err.Error())))
	}
	cCtx, cancel := context.WithTimeout(ctx.Context(), 25*time.Second)
	defer cancel()
	subID, err := p.client.RequestOTP(cCtx, msisdn)
	if err != nil {
		return p.assistantV2Rearm(ctx, state, fmt.Sprintf("❌ <b>Gagal meminta OTP dari MyXL:</b>\n<code>%s</code>", html.EscapeString(err.Error())))
	}
	existing, _ := p.repo.GetByMSISDN(cCtx, msisdn)
	if existing == nil {
		existing = &Account{MSISDN: msisdn, SubscriberID: subID}
	} else if subID != "" {
		existing.SubscriberID = subID
	}
	if err := p.repo.Save(cCtx, existing); err != nil {
		return p.assistantV2Rearm(ctx, state, fmt.Sprintf("❌ Gagal menyimpan sesi login: <code>%s</code>", html.EscapeString(err.Error())))
	}
	state.Wizard = "login_otp"
	state.MSISDN = msisdn
	prompt := fmt.Sprintf(
		"📩 <b>Login MyXL — Langkah 2 dari 2</b>\n\nKode OTP 6-digit telah dikirim via SMS ke <code>%s</code>.\n\nSilakan kirimkan <b>6 digit kode OTP</b> Anda sekarang.\n\n<i>Ketik <code>/cancel</code> untuk membatalkan.</i>",
		html.EscapeString(msisdn),
	)
	return p.assistantV2Await(ctx, state, prompt)
}

func (p *Plugin) assistantV2InputOTP(ctx *orchestration.Context, state assistantV2State, input string) error {
	code, err := normalizeOTPCode(input)
	if err != nil {
		return p.assistantV2Rearm(ctx, state, "⚠️ <b>Kode OTP harus berupa 6 digit angka.</b>")
	}
	cCtx, cancel := context.WithTimeout(ctx.Context(), 25*time.Second)
	defer cancel()
	tokens, err := p.client.SubmitOTP(cCtx, state.MSISDN, code)
	if err != nil {
		return p.assistantV2Rearm(ctx, state, fmt.Sprintf("❌ <b>Verifikasi OTP gagal:</b>\n<code>%s</code>", html.EscapeString(err.Error())))
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
		return p.assistantV2Rearm(ctx, state, fmt.Sprintf("⚠️ Login berhasil di CIAM tetapi gagal disimpan: <code>%s</code>", html.EscapeString(err.Error())))
	}
	screen, err := p.menuMgr.BuildDashboardScreen(cCtx, false)
	if err != nil {
		return err
	}
	return p.assistantV2Transition(ctx, assistantV2State{}, screen)
}

func (p *Plugin) assistantV2InputAlias(ctx *orchestration.Context, state assistantV2State, input string) error {
	alias, err := normalizeAlias(input)
	if err != nil {
		return p.assistantV2Rearm(ctx, state, "⚠️ "+html.EscapeString(err.Error())+".")
	}
	if err := p.repo.SetAlias(ctx.Context(), state.MSISDN, alias); err != nil {
		return p.assistantV2Rearm(ctx, state, fmt.Sprintf("❌ Gagal menyimpan alias: %v", err))
	}
	screen, err := p.menuMgr.BuildAccountsScreen(ctx.Context())
	if err != nil {
		return err
	}
	return p.assistantV2Transition(ctx, assistantV2State{}, screen)
}

func (p *Plugin) assistantV2InputOptionCode(ctx *orchestration.Context, state assistantV2State, input string) error {
	optionCode := strings.TrimSpace(input)
	if optionCode == "" {
		return p.assistantV2Rearm(ctx, state, "⚠️ Kode paket tidak boleh kosong.")
	}
	acc, err := p.repo.GetActive(ctx.Context())
	if err != nil || acc == nil {
		return p.assistantV2Rearm(ctx, state, "❌ Tidak ada akun MyXL aktif.")
	}
	screen, err := p.menuMgr.BuildPackageDetailScreen(ctx.Context(), acc, optionCode)
	if err != nil {
		return p.assistantV2Rearm(ctx, state, fmt.Sprintf("❌ Gagal memuat paket <code>%s</code>: <code>%s</code>", html.EscapeString(optionCode), html.EscapeString(err.Error())))
	}
	return p.assistantV2Transition(ctx, assistantV2State{}, screen)
}

func (p *Plugin) assistantV2InputCustomPrice(ctx *orchestration.Context, state assistantV2State, input string) error {
	value, err := strconv.ParseInt(strings.TrimSpace(input), 10, 64)
	if err != nil || value < 0 {
		return p.assistantV2Rearm(ctx, state, "⚠️ Nominal harga tidak valid. Masukkan angka bulat non-negatif.")
	}
	acc, err := p.repo.GetActive(ctx.Context())
	if err != nil || acc == nil {
		return p.assistantV2Rearm(ctx, state, "❌ Tidak ada akun MyXL aktif.")
	}
	details, err := p.client.GetPackageDetails(ctx.Context(), acc, state.OptionCode)
	if err != nil || details.TokenConfirmation == "" {
		return p.assistantV2Rearm(ctx, state, "❌ Gagal memuat token konfirmasi paket.")
	}
	pkgName := state.OptionCode
	var price int64
	if details.PackageOption != nil {
		pkgName = details.PackageOption.Name
		price = int64(details.PackageOption.Price)
	}
	draft := purchaseDraftState{
		MSISDN: acc.MSISDN, OptionCode: state.OptionCode, PackageName: pkgName, Price: price,
		TokenConfirmation: details.TokenConfirmation, Method: state.Method, WalletNumber: acc.MSISDN,
		OverwritePrice: value, HasOverwrite: true,
	}
	state.Wizard = ""
	state.Draft = &draft
	screen, err := p.menuMgr.BuildCheckoutScreen(draft)
	if err != nil {
		return err
	}
	return p.assistantV2Transition(ctx, state, screen)
}

func (p *Plugin) assistantV2InputFamily(ctx *orchestration.Context, state assistantV2State, input string) error {
	familyCode := strings.TrimSpace(input)
	if familyCode == "" {
		return p.assistantV2Rearm(ctx, state, "⚠️ Family Code tidak boleh kosong.")
	}
	acc, err := p.repo.GetActive(ctx.Context())
	if err != nil || acc == nil {
		return p.assistantV2Rearm(ctx, state, "❌ Tidak ada akun aktif terhubung.")
	}
	screen, err := p.menuMgr.BuildFamilyPackagesScreen(ctx.Context(), acc, familyCode, 1)
	if err != nil {
		return p.assistantV2Rearm(ctx, state, fmt.Sprintf("⚠️ <b>Gagal mencari paket:</b> %v", err))
	}
	return p.assistantV2Transition(ctx, assistantV2State{}, screen)
}

func (p *Plugin) confirmAssistantV2Purchase(ctx *orchestration.Context, state assistantV2State, draft purchaseDraftState) error {
	cCtx, cancel := context.WithTimeout(ctx.Context(), 45*time.Second)
	defer cancel()

	acc, err := p.repo.GetByMSISDN(cCtx, draft.MSISDN)
	if err != nil || acc == nil {
		return ctx.Answer("Akun untuk draft pembelian tidak ditemukan.", true)
	}
	tokenKey := draft.TokenConfirmation
	if tokenKey == "" {
		tokenKey = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	tokenHash := sha256.Sum256([]byte(tokenKey))
	key := fmt.Sprintf("%s:%s:%x", draft.MSISDN, draft.OptionCode, tokenHash[:16])

	reserved, err := p.repo.ReservePurchase(cCtx, key, draft.MSISDN, draft.OptionCode, draft.Method)
	if err != nil {
		return ctx.Answer("Gagal mengamankan transaksi. Pembelian tidak dijalankan.", true)
	}
	if !reserved {
		return ctx.Answer("Transaksi sedang diproses atau baru saja dikonfirmasi.", true)
	}

	item := PurchaseItem{
		ItemCode: draft.OptionCode, ItemPrice: draft.Price, ItemName: draft.PackageName,
		TokenConfirmation: draft.TokenConfirmation,
	}
	var overwrite *int64
	if draft.HasOverwrite {
		overwrite = &draft.OverwritePrice
	}
	var result *SettlementResult
	switch draft.Method {
	case "balance":
		result, err = p.client.SettlementBalance(cCtx, acc, item, overwrite)
	case "qris":
		result, err = p.client.SettlementQRIS(cCtx, acc, item, overwrite)
	case "gopay", "ovo", "dana", "shopeepay":
		result, err = p.client.SettlementMultipayment(cCtx, acc, item, strings.ToUpper(draft.Method), draft.WalletNumber, overwrite)
	case "decoy_balance":
		result, err = p.client.SettlementDecoy(cCtx, acc, item, "balance", overwrite)
	case "decoy_qris":
		result, err = p.client.SettlementDecoy(cCtx, acc, item, "qris", overwrite)
	case "decoy_qris0":
		result, err = p.client.SettlementDecoy(cCtx, acc, item, "qris0", overwrite)
	default:
		err = fmt.Errorf("unsupported payment method %q", draft.Method)
	}
	if err != nil {
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
		_ = p.repo.FinishPurchase(persistCtx, key, "UNKNOWN", "", err.Error())
		persistCancel()
		return ctx.Answer("Hasil transaksi tidak dapat dipastikan. Periksa riwayat MyXL sebelum mencoba lagi.", true)
	}
	if result == nil {
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
		_ = p.repo.FinishPurchase(persistCtx, key, "UNKNOWN", "", "empty settlement result")
		persistCancel()
		return ctx.Answer("Hasil transaksi kosong dan tidak dapat dipastikan.", true)
	}

	status := "FAILED"
	if result.IsSuccess {
		status = "SUCCESS"
	}
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
	finishErr := p.repo.FinishPurchase(persistCtx, key, status, result.TransactionCode, result.Message)
	persistCancel()
	if finishErr != nil {
		return ctx.Answer("Transaksi selesai tetapi hasilnya gagal dicatat. Periksa riwayat MyXL.", true)
	}

	effectivePrice := draft.Price
	if draft.HasOverwrite {
		effectivePrice = draft.OverwritePrice
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
				IdempotencyKey:  key,
				MSISDN:          draft.MSISDN,
				OptionCode:      draft.OptionCode,
				PackageName:     draft.PackageName,
				Price:           effectivePrice,
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
	screen := p.menuMgr.BuildPurchaseResultScreen(result, draft.PackageName, effectivePrice, draft.Method, draft.OptionCode)
	if err := p.assistantV2Transition(ctx, state, screen); err != nil {
		return err
	}

	if result.QRCode != "" {
		rt := p.assistantRuntime()
		if target, ok := ctx.Target().(presentationtelegram.MessageTarget); ok && target.Peer != nil && rt.Service != nil {
			if err := p.sendQRPhoto(ctx.Context(), rt.Service, target.Peer, result.QRCode, draft.PackageName, effectivePrice); err != nil && qrWarning == "" {
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

var _ assistantinteraction.V2FeatureDriver = (*Plugin)(nil)
var _ interface{ FeatureSpec() feature.Spec } = (*Plugin)(nil)

