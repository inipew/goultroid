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

	nativePurchaseScreen        = "native_purchase_confirm"
	nativePurchaseConfirmAction = "native_purchase_confirm"
	nativePurchaseCancelAction  = "native_purchase_cancel"
	nativePurchaseConfirmExec   = 55 * time.Second
	nativePurchaseProcessingTTL = 2 * time.Minute
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

	registrations := make([]interface{ Close() }, 0, 3)
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

	balance, balanceErr := p.client.GetBalance(queryCtx, acc)
	quota, quotaErr := p.client.GetQuotaDetails(queryCtx, acc)
	if balanceErr != nil && quotaErr != nil {
		return ctx.Answer("Gagal memperbarui pulsa dan kuota MyXL. Silakan coba lagi.", true)
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return ctx.Transition(raw, nativeQuotaRefreshTTL, nativeQuotaView(FormatQuotaResponse(acc, balance, quota, state.Masked)))
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

	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	if err := ctx.Transition(raw, nativePurchaseProcessingTTL, presentation.View{
		Text: fmt.Sprintf(
			"⏳ <b>Memproses pembelian MyXL</b>\n\nPaket: <b>%s</b>\nKode: <code>%s</code>\nMetode: <code>%s</code>\n\nJangan ulangi transaksi sampai hasil akhir ditampilkan.",
			html.EscapeString(resolved.PackageName),
			html.EscapeString(resolved.Intent.OptionCode),
			html.EscapeString(strings.ToUpper(resolved.Intent.Method)),
		),
	}); err != nil {
		return err
	}

	reserved, err := p.repo.ReservePurchase(
		cCtx,
		resolved.IdempotencyKey,
		resolved.Intent.MSISDN,
		resolved.Intent.OptionCode,
		resolved.Intent.Method,
	)
	if err != nil {
		return ctx.Terminate(presentation.View{Text: "❌ Gagal mengamankan transaksi. Pembelian tidak dijalankan."})
	}
	if !reserved {
		return ctx.Terminate(presentation.View{Text: "⏳ Transaksi sedang diproses atau baru saja dikonfirmasi. Pembelian tidak dijalankan ulang."})
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
		return ctx.Terminate(presentation.View{Text: "⚠️ Hasil transaksi tidak dapat dipastikan. Transaksi tidak akan diulang otomatis; periksa riwayat MyXL sebelum mencoba lagi."})
	}
	if result == nil {
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
		_ = p.repo.FinishPurchase(persistCtx, resolved.IdempotencyKey, "UNKNOWN", "", "empty settlement result")
		persistCancel()
		return ctx.Terminate(presentation.View{Text: "⚠️ Hasil transaksi kosong dan tidak dapat dipastikan. Periksa riwayat MyXL sebelum mencoba lagi."})
	}

	status := "FAILED"
	if result.IsSuccess {
		status = "SUCCESS"
	}
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
	finishErr := p.repo.FinishPurchase(persistCtx, resolved.IdempotencyKey, status, result.TransactionCode, result.Message)
	persistCancel()
	if finishErr != nil {
		return ctx.Terminate(presentation.View{Text: "⚠️ Transaksi selesai tetapi hasilnya gagal dicatat. Periksa riwayat MyXL sebelum mencoba lagi."})
	}

	var qrWarning string
	if result.QRCode != "" {
		qrPayload, qrErr := normalizeQRPayload(result.QRCode)
		if qrErr != nil {
			qrWarning = "\n\n⚠️ Payload QRIS dari operator tidak valid; gambar QR tidak dibuat."
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
				qrWarning = "\n\n⚠️ QRIS berhasil dibuat tetapi gagal disimpan untuk dilihat kembali."
			}
		}
	}

	text := FormatPurchaseResult(result, resolved.PackageName, resolved.EffectivePrice, strings.ToUpper(resolved.Intent.Method))
	if result.QRCode != "" {
		text += "\n\n<i>Gunakan <code>.myxl qris</code> bila ingin melihat kembali QRIS aktif atau mengirim gambar QR.</i>"
	}
	text += qrWarning
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
