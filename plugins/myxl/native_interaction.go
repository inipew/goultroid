package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	nativeQuotaScreen        = "native_quota"
	nativeQuotaRefreshAction = "native_quota_refresh"
	nativeQuotaRefreshTTL    = 10 * time.Minute
	nativeQuotaRefreshExec   = 30 * time.Second

	nativePurchaseScreen             = "native_purchase_confirm"
	nativePurchaseConfirmAction      = "native_purchase_confirm"
	nativePurchaseCancelAction       = "native_purchase_cancel"
	nativePurchaseConfirmExec        = 55 * time.Second
	nativeNotificationsScreen        = "native_notifications"
	nativeNotificationsRefreshAction = "native_notif_refresh"
	nativeNotificationsReadAction    = "native_notif_read"
)

type nativeRuntimeState struct {
	mu      sync.RWMutex
	runtime nativeinteraction.DriverRuntime
}

func (p *Plugin) NativeFeatureID() string { return p.Name() }

func (p *Plugin) BindNative(rt nativeinteraction.DriverRuntime) (func(), error) {
	if p == nil || rt.Interactions == nil || rt.Catalog == nil || rt.Scope.IsZero() {
		return nil, nativeinteraction.ErrUnavailable
	}
	scope, ok := rt.Catalog.FeatureScope(p.Name())
	if !ok || scope != rt.Scope {
		return nil, fmt.Errorf("myxl: native feature scope unavailable")
	}

	registrations := make([]interface{ Close() }, 0, 5)
	registerPrepared := func(actionID string, timeout time.Duration, handler orchestration.Handler) error {
		registration, err := rt.Interactions.RegisterPreparedAction(
			rt.Scope,
			p.Name(),
			actionID,
			func(context.Context, rootinteraction.Action) (rootinteraction.ActionAdmission, error) {
				return rootinteraction.ActionAdmission{
					Scope: rt.Scope,
					Profile: tasks.ExecutionProfile{
						ExecutionTimeout: timeout,
					},
				}, nil
			},
			handler,
		)
		if err != nil {
			return err
		}
		registrations = append(registrations, registration)
		return nil
	}

	if err := registerPrepared(nativeQuotaRefreshAction, nativeQuotaRefreshExec, p.handleNativeQuotaRefresh); err != nil {
		return nil, fmt.Errorf("myxl: register native quota refresh: %w", err)
	}
	if err := registerPrepared(nativePurchaseConfirmAction, nativePurchaseConfirmExec, p.handleNativePurchaseConfirm); err != nil {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
		return nil, fmt.Errorf("myxl: register native purchase confirm: %w", err)
	}
	for _, entry := range []struct {
		id      string
		handler orchestration.Handler
	}{
		{nativeNotificationsRefreshAction, p.handleNativeNotificationsRefresh},
		{nativeNotificationsReadAction, p.handleNativeNotificationsRead},
	} {
		if err := registerPrepared(entry.id, 55*time.Second, entry.handler); err != nil {
			for i := len(registrations) - 1; i >= 0; i-- {
				registrations[i].Close()
			}
			return nil, fmt.Errorf("myxl: register native notifications: %w", err)
		}
	}
	cancelRegistration, err := rt.Interactions.RegisterAction(
		rt.Scope,
		p.Name(),
		nativePurchaseCancelAction,
		p.handleNativePurchaseCancel,
	)
	if err != nil {
		for i := len(registrations) - 1; i >= 0; i-- {
			registrations[i].Close()
		}
		return nil, fmt.Errorf("myxl: register native purchase cancel: %w", err)
	}
	registrations = append(registrations, cancelRegistration)

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

func (p *Plugin) currentNativeRuntime() nativeinteraction.DriverRuntime {
	if p == nil {
		return nativeinteraction.DriverRuntime{}
	}
	p.native.mu.RLock()
	rt := p.native.runtime
	p.native.mu.RUnlock()
	return rt
}

func (p *Plugin) nativeQuotaAvailable() bool {
	rt := p.currentNativeRuntime()
	return rt.Interactions != nil && !rt.Scope.IsZero()
}

func (p *Plugin) nativePurchaseAvailable() bool {
	rt := p.currentNativeRuntime()
	return rt.Interactions != nil && !rt.Scope.IsZero()
}

func (p *Plugin) openNativeQuotaRefresh(cmd *core.Context, state quotaRefreshState, text string) (bool, error) {
	rt := p.currentNativeRuntime()
	if rt.Interactions == nil || rt.Scope.IsZero() {
		return false, nil
	}
	if cmd == nil || strings.TrimSpace(state.MSISDN) == "" {
		return true, nativeinteraction.ErrInvalidInvocation
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return true, fmt.Errorf("myxl: encode native quota state: %w", err)
	}
	_, err = rt.Interactions.Begin(cmd, nativeinteraction.BeginRequest{
		FeatureID: p.Name(),
		ScreenID:  nativeQuotaScreen,
		State:     raw,
		TTL:       nativeQuotaRefreshTTL,
		View:      nativeQuotaView(text),
	})
	return true, err
}

func (p *Plugin) openNativePurchaseConfirmation(
	cmd *core.Context,
	intent purchaseIntentState,
	quote purchaseCheckoutPreview,
) (bool, error) {
	rt := p.currentNativeRuntime()
	if rt.Interactions == nil || rt.Scope.IsZero() {
		return false, nil
	}
	if cmd == nil {
		return true, nativeinteraction.ErrInvalidInvocation
	}
	intent, err := normalizePurchaseIntent(intent)
	if err != nil {
		return true, err
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return true, fmt.Errorf("myxl: encode native purchase intent: %w", err)
	}
	_, err = rt.Interactions.Begin(cmd, nativeinteraction.BeginRequest{
		FeatureID: p.Name(),
		ScreenID:  nativePurchaseScreen,
		State:     raw,
		TTL:       purchaseConfirmationTTL,
		View:      nativePurchaseConfirmationView(intent, quote),
	})
	return true, err
}

func (p *Plugin) handleNativeQuotaRefresh(ctx *orchestration.Context) error {
	if p == nil || ctx == nil {
		return orchestration.ErrInvalidEngine
	}
	state, err := decodeNativeQuotaState(ctx.State())
	if err != nil {
		return ctx.Answer("Tombol refresh tidak valid atau sudah kedaluwarsa.", true)
	}

	queryCtx, cancel := context.WithTimeout(ctx.Context(), 25*time.Second)
	defer cancel()
	acc, err := p.repo.GetByMSISDN(queryCtx, state.MSISDN)
	if err != nil {
		return ctx.Answer("Gagal membaca akun MyXL. Silakan coba lagi.", true)
	}
	if acc == nil {
		return ctx.Answer("Akun MyXL tidak ditemukan. Buka ulang .kuota.", true)
	}

	snapshot := p.loadQuotaSnapshot(queryCtx, acc)
	if snapshot.allFailed() {
		return ctx.Answer("Gagal memperbarui pulsa dan kuota MyXL. Silakan coba lagi.", true)
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return ctx.Transition(raw, nativeQuotaRefreshTTL, nativeQuotaView(FormatQuotaSnapshot(acc, snapshot, state.Masked)))
}
func (p *Plugin) handleNativePurchaseCancel(ctx *orchestration.Context) error {
	if p == nil || ctx == nil {
		return orchestration.ErrInvalidEngine
	}
	return ctx.Terminate(presentation.View{Text: "✅ Pembelian dibatalkan. Tidak ada transaksi yang dijalankan."})
}

func (p *Plugin) handleNativePurchaseConfirm(ctx *orchestration.Context) error {
	if p == nil || ctx == nil {
		return orchestration.ErrInvalidEngine
	}
	intent, err := decodeNativePurchaseIntent(ctx.State())
	if err != nil {
		return ctx.Terminate(presentation.View{Text: "❌ Draft pembelian tidak valid atau sudah kedaluwarsa. Buka ulang paket lalu coba lagi."})
	}

	cCtx, cancel := context.WithTimeout(ctx.Context(), 45*time.Second)
	defer cancel()
	resolved, err := p.resolvePurchaseIntent(cCtx, intent)
	if err != nil {
		if errors.Is(err, ErrPurchaseQuoteChanged) {
			return ctx.Terminate(presentation.View{Text: "⚠️ Harga paket berubah sejak halaman konfirmasi dibuat. Pembelian tidak dijalankan; buka ulang paket untuk mengonfirmasi harga terbaru."})
		}
		return ctx.Terminate(presentation.View{Text: "❌ Detail pembelian tidak lagi valid. Pembelian tidak dijalankan; buka ulang paket lalu coba lagi."})
	}

	raw, err := json.Marshal(resolved.Intent)
	if err != nil {
		return err
	}
	if err := ctx.Transition(raw, purchaseProcessingTTL, presentation.View{
		Text: fmt.Sprintf(
			"⏳ <b>Memproses pembelian MyXL</b>\n\nPaket: <b>%s</b>\nKode: <code>%s</code>\nMetode: <code>%s</code>\n\nJangan ulangi transaksi sampai status akhir ditampilkan.",
			html.EscapeString(resolved.PackageName),
			html.EscapeString(resolved.Intent.OptionCode),
			html.EscapeString(strings.ToUpper(resolved.Intent.Method)),
		),
	}); err != nil {
		return err
	}

	execution, err := p.executeResolvedPurchase(cCtx, resolved)
	if err != nil {
		return ctx.Terminate(presentation.View{Text: "❌ Gagal mengamankan transaksi. Pembelian tidak dijalankan."})
	}
	switch execution.Kind {
	case purchaseOutcomeDuplicate:
		return ctx.Terminate(presentation.View{Text: "⏳ Transaksi sedang diproses atau baru saja dikonfirmasi. Klik ulang tidak menjalankan pembelian kedua."})
	case purchaseOutcomeUnknown:
		return ctx.Terminate(presentation.View{Text: "⚠️ Hasil transaksi tidak dapat dipastikan. Transaksi tidak akan diulang otomatis; periksa riwayat MyXL sebelum mencoba lagi."})
	}

	text := FormatPurchaseResult(
		execution.Result,
		resolved.PackageName,
		resolved.EffectivePrice,
		strings.ToUpper(resolved.Intent.Method),
	)
	if execution.Kind == purchaseOutcomePendingQRIS {
		text += "\n<i>Gunakan <code>.myxl qris</code> untuk membuka kembali tagihan QRIS yang tersimpan di bot.</i>"
	}
	if execution.Warning != "" {
		text += "\n\n⚠️ " + html.EscapeString(execution.Warning)
	}
	return ctx.Terminate(presentation.View{Text: text})
}
func decodeNativeQuotaState(raw []byte) (quotaRefreshState, error) {
	var state quotaRefreshState
	if len(raw) == 0 {
		return state, fmt.Errorf("myxl: empty native quota state")
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return quotaRefreshState{}, fmt.Errorf("myxl: decode native quota state: %w", err)
	}
	state.MSISDN = strings.TrimSpace(state.MSISDN)
	if state.MSISDN == "" {
		return quotaRefreshState{}, fmt.Errorf("myxl: native quota account is empty")
	}
	return state, nil
}

func decodeNativePurchaseIntent(raw []byte) (purchaseIntentState, error) {
	var intent purchaseIntentState
	if len(raw) == 0 {
		return intent, ErrPurchaseIntentInvalid
	}
	if err := json.Unmarshal(raw, &intent); err != nil {
		return purchaseIntentState{}, fmt.Errorf("%w: decode native purchase intent", ErrPurchaseIntentInvalid)
	}
	return normalizePurchaseIntent(intent)
}

func nativeQuotaView(text string) presentation.View {
	return presentation.View{
		Text: text,
		Rows: []presentation.Row{{
			presentation.ActionButton("🔄 Perbarui Kuota", nativeQuotaRefreshAction),
		}},
	}
}

func nativeNotificationsView(text string) presentation.View {
	return presentation.View{Text: text, Rows: []presentation.Row{{
		presentation.ActionButton("📖 Tandai Semua Dibaca", nativeNotificationsReadAction),
		presentation.ActionButton("🔄 Muat Ulang", nativeNotificationsRefreshAction),
	}}}
}

func (p *Plugin) openNativeNotifications(cmd *core.Context, msisdn, content string) (bool, error) {
	rt := p.currentNativeRuntime()
	if rt.Interactions == nil || rt.Scope.IsZero() {
		return false, nil
	}
	if cmd == nil || msisdn == "" {
		return true, nativeinteraction.ErrInvalidInvocation
	}
	_, err := rt.Interactions.Begin(cmd, nativeinteraction.BeginRequest{
		FeatureID: p.Name(), ScreenID: nativeNotificationsScreen,
		State: []byte(msisdn), TTL: 10 * time.Minute, View: nativeNotificationsView(content),
	})
	return true, err
}

func (p *Plugin) handleNativeNotificationsRefresh(ctx *orchestration.Context) error {
	return p.nativeNotificationsTransition(ctx, false)
}

func (p *Plugin) handleNativeNotificationsRead(ctx *orchestration.Context) error {
	return p.nativeNotificationsTransition(ctx, true)
}

func (p *Plugin) nativeNotificationsTransition(ctx *orchestration.Context, markRead bool) error {
	msisdn := strings.TrimSpace(string(ctx.State()))
	if msisdn == "" {
		return ctx.Answer("Sesi notifikasi tidak valid.", true)
	}
	queryCtx, cancel := context.WithTimeout(ctx.Context(), 50*time.Second)
	defer cancel()
	acc, err := p.repo.GetByMSISDN(queryCtx, msisdn)
	if err != nil || acc == nil {
		return ctx.Answer("Akun MyXL tidak tersedia.", true)
	}
	items, err := p.client.GetNotifications(queryCtx, acc)
	if err != nil {
		return ctx.Answer("Gagal memuat notifikasi MyXL.", true)
	}
	if markRead {
		read, failed := p.client.ReadAllNotifications(queryCtx, acc, items)
		if failed > 0 {
			_ = ctx.Answer(fmt.Sprintf("%d dibaca, %d gagal.", read, failed), true)
		} else {
			_ = ctx.Answer(fmt.Sprintf("%d notifikasi ditandai dibaca.", read), false)
		}
		items, err = p.client.GetNotifications(queryCtx, acc)
		if err != nil {
			return ctx.Answer("Gagal memuat ulang notifikasi MyXL.", true)
		}
	}
	content := FormatNotificationsLimited(items, 3800)
	return ctx.Transition([]byte(msisdn), 10*time.Minute, nativeNotificationsView(content))
}

func nativePurchaseConfirmationView(intent purchaseIntentState, quote purchaseCheckoutPreview) presentation.View {
	return presentation.View{
		Text: fmt.Sprintf(
			"⚠️ <b>Konfirmasi Pembelian MyXL</b>\n\n<b>Paket:</b> %s\n<b>Kode:</b> <code>%s</code>\n<b>Metode:</b> <code>%s</code>\n<b>Nominal:</b> Rp %s\n\nHarga canonical akan diverifikasi ulang saat Konfirmasi ditekan. Transaksi hanya dapat dikonfirmasi sekali.",
			html.EscapeString(quote.PackageName),
			html.EscapeString(intent.OptionCode),
			html.EscapeString(strings.ToUpper(intent.Method)),
			formatRupiah(quote.EffectivePrice),
		),
		Rows: []presentation.Row{{
			presentation.ActionButton("✅ Konfirmasi", nativePurchaseConfirmAction),
			presentation.ActionButton("❌ Batal", nativePurchaseCancelAction),
		}},
	}
}

var _ nativeinteraction.FeatureDriver = (*Plugin)(nil)
