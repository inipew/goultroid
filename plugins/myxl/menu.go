package myxl

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	coreCallback "github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/ui"
	"github.com/inipew/goultroid/internal/ui/render"
)

const (
	wizardTTL = 2 * time.Minute

	wizardLoginMSISDN = 1
	wizardLoginOTP    = 2
	wizardSetAlias    = 3
	wizardOptionCode  = 4
	wizardCustomPrice = 5
	wizardFamilyCode  = 6
)

type wizardSession struct {
	Type        int
	MSISDN      string
	Target      interaction.MessageTarget
	OptionCode  string
	PackageName string
	Price       int64
	Method      string
	ExpiresAt   time.Time
}

type MenuManager struct {
	plugin     *Plugin
	menuCtrl   *menu.Controller
	sessionsMu sync.Mutex
	sessions   map[int64]*wizardSession
}

func NewMenuManager(p *Plugin, ctrl *menu.Controller) *MenuManager {
	m := &MenuManager{
		plugin:   p,
		menuCtrl: ctrl,
		sessions: make(map[int64]*wizardSession),
	}
	if ctrl != nil {
		ctrl.RegisterTextHandler(m)
	}
	return m
}

// RegisterOptionCode returns a compact key for optionCode safe for Telegram's 64-byte callback limit.
func (m *MenuManager) RegisterOptionCode(optCode string) string {
	if optCode == "" {
		return ""
	}
	if len(optCode) <= 24 && !strings.Contains(optCode, ":") {
		return optCode
	}

	if m.plugin != nil && m.plugin.stateStore != nil {
		return m.plugin.stateStore.StoreWithScope(optCode, coreCallback.StateScope{
			Namespace: m.plugin.Namespace(),
		}, 24*time.Hour)
	}
	return ""
}

// ResolveOptionCode resolves an option key or raw code back to the canonical full option code.
func (m *MenuManager) ResolveOptionCode(keyOrCode string) string {
	if keyOrCode == "" {
		return ""
	}
	if m.plugin != nil && m.plugin.stateStore != nil {
		if val, _, ok := m.plugin.stateStore.Get(keyOrCode); ok {
			if s, ok := val.(string); ok && s != "" {
				return s
			}
		}
	}
	return keyOrCode
}

// RegisterQR registers a QR payload in the state store with a short key safe for callback data.
func (m *MenuManager) RegisterQR(qrPayload string) string {
	if qrPayload == "" {
		return ""
	}
	if m.plugin != nil && m.plugin.stateStore != nil {
		return m.plugin.stateStore.StoreWithScope(qrPayload, coreCallback.StateScope{
			Namespace: m.plugin.Namespace(),
		}, 24*time.Hour)
	}
	return ""
}

// ResolveQR resolves a registered QR key back to the raw QR payload string.
func (m *MenuManager) ResolveQR(key string) string {
	if key == "" {
		return ""
	}
	if m.plugin != nil && m.plugin.stateStore != nil {
		if val, _, ok := m.plugin.stateStore.Get(key); ok {
			if s, ok := val.(string); ok && s != "" {
				return s
			}
		}
	}
	return key
}

func (m *MenuManager) SetSession(userID int64, sess *wizardSession) {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()
	if sess == nil {
		delete(m.sessions, userID)
		return
	}
	sess.ExpiresAt = time.Now().Add(wizardTTL)
	m.sessions[userID] = sess
}

func (m *MenuManager) GetSession(userID int64) (*wizardSession, bool) {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()
	sess, ok := m.sessions[userID]
	if !ok || sess == nil {
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		delete(m.sessions, userID)
		return nil, false
	}
	return sess, true
}

func (m *MenuManager) ClearSession(userID int64) {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()
	delete(m.sessions, userID)
}

func (m *MenuManager) registerMessageInstance(userID int64, msg *tg.Message) {
	if m.menuCtrl == nil || msg == nil || msg.ID == 0 {
		return
	}
	chatID := userID
	switch p := msg.PeerID.(type) {
	case *tg.PeerUser:
		chatID = p.UserID
	case *tg.PeerChat:
		chatID = p.ChatID
	case *tg.PeerChannel:
		chatID = p.ChannelID
	}
	m.menuCtrl.RegisterInstance(menu.MenuInstance{
		ID:        fmt.Sprintf("menu:%d:%d", chatID, msg.ID),
		OwnerID:   userID,
		ChatID:    chatID,
		MessageID: msg.ID,
		Screen:    menu.ScreenIDMyXL,
	})
}

// HandleTextMessage implements menu.TextHandler for multi-step wizards.
func (m *MenuManager) HandleTextMessage(ctx context.Context, userID, chatID int64, text string, inter interaction.MessageInteraction) (bool, error) {
	if userID == 0 || inter == nil {
		return false, nil
	}
	sess, ok := m.GetSession(userID)
	if !ok {
		return false, nil
	}

	trimmed := strings.TrimSpace(text)
	if strings.EqualFold(trimmed, "/cancel") {
		m.ClearSession(userID)
		_, err := inter.SendMessage(ctx, sess.Target.Peer(), "❌ Interaksi MyXL dibatalkan.\n\nKetik /start atau buka menu kembali.", nil)
		return true, err
	}
	if strings.HasPrefix(trimmed, "/") {
		return false, nil
	}

	switch sess.Type {
	case wizardLoginMSISDN:
		return m.handleWizardLoginMSISDN(ctx, userID, trimmed, sess, inter)
	case wizardLoginOTP:
		return m.handleWizardLoginOTP(ctx, userID, trimmed, sess, inter)
	case wizardSetAlias:
		return m.handleWizardSetAlias(ctx, userID, trimmed, sess, inter)
	case wizardOptionCode:
		return m.handleWizardOptionCode(ctx, userID, trimmed, sess, inter)
	case wizardCustomPrice:
		return m.handleWizardCustomPrice(ctx, userID, trimmed, sess, inter)
	case wizardFamilyCode:
		return m.handleWizardFamilyCode(ctx, userID, trimmed, sess, inter)
	default:
		m.ClearSession(userID)
		return false, nil
	}
}

func (m *MenuManager) handleWizardLoginMSISDN(ctx context.Context, userID int64, input string, sess *wizardSession, inter interaction.MessageInteraction) (bool, error) {
	msisdn, err := NormalizeMSISDN(input)
	if err != nil {
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			fmt.Sprintf("⚠️ <b>Nomor HP tidak valid:</b> %v\n\nContoh: <code>081912345678</code> atau <code>6281912345678</code>.\nKirimkan ulang atau ketik <code>/cancel</code> untuk batal.", err),
			nil)
		return true, sendErr
	}

	_, _ = inter.SendMessage(ctx, sess.Target.Peer(),
		fmt.Sprintf("⏳ Mengirimkan kode verifikasi OTP ke <code>%s</code>...", html.EscapeString(msisdn)),
		nil)

	cCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	subID, reqErr := m.plugin.client.RequestOTP(cCtx, msisdn)
	if reqErr != nil {
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			fmt.Sprintf("❌ <b>Gagal meminta OTP dari MyXL:</b>\n<code>%s</code>\n\nSilakan coba lagi beberapa saat lagi.", html.EscapeString(reqErr.Error())),
			nil)
		m.ClearSession(userID)
		return true, sendErr
	}

	// Update or create placeholder account
	existing, _ := m.plugin.repo.GetByMSISDN(cCtx, msisdn)
	if existing == nil {
		existing = &Account{MSISDN: msisdn, SubscriberID: subID}
	} else if subID != "" {
		existing.SubscriberID = subID
	}
	_ = m.plugin.repo.Save(cCtx, existing)

	sess.Type = wizardLoginOTP
	sess.MSISDN = msisdn
	m.SetSession(userID, sess)

	prompt := fmt.Sprintf(
		"📩 <b>Login MyXL — Langkah 2 dari 2</b>\n\n"+
			"Kode OTP 6-digit telah dikirim via SMS ke <code>%s</code>.\n\n"+
			"Silakan kirimkan <b>6 digit kode OTP</b> Anda sekarang.\n\n"+
			"<i>Ketik <code>/cancel</code> atau tekan Batal di bawah jika ingin membatalkan.</i>",
		html.EscapeString(msisdn),
	)
	markup := render.ToTelegramMarkup(ui.Markup{Rows: []ui.ButtonRow{{
		ui.NewCallbackButton("🔄 Kirim Ulang OTP", []byte(fmt.Sprintf("a1:myxl:resend_otp:%s", msisdn))),
		ui.NewCallbackButton("❌ Batal", []byte("a1:myxl:cancel_wizard")),
	}}})
	msg, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), prompt, markup)
	if sendErr == nil {
		m.registerMessageInstance(userID, msg)
	}
	return true, sendErr
}

func (m *MenuManager) handleWizardLoginOTP(ctx context.Context, userID int64, input string, sess *wizardSession, inter interaction.MessageInteraction) (bool, error) {
	code := strings.TrimSpace(input)
	if len(code) != 6 {
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			"⚠️ <b>Kode OTP harus berupa 6 digit angka!</b>\n\nSilakan kirimkan ulang atau ketik <code>/cancel</code>.",
			nil)
		return true, sendErr
	}

	_, _ = inter.SendMessage(ctx, sess.Target.Peer(),
		fmt.Sprintf("⏳ Memverifikasi kode OTP untuk <code>%s</code>...", html.EscapeString(sess.MSISDN)),
		nil)

	cCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	tokens, err := m.plugin.client.SubmitOTP(cCtx, sess.MSISDN, code)
	if err != nil {
		markup := render.ToTelegramMarkup(ui.Markup{Rows: []ui.ButtonRow{{
			ui.NewCallbackButton("🔄 Kirim Ulang OTP", []byte(fmt.Sprintf("a1:myxl:resend_otp:%s", sess.MSISDN))),
			ui.NewCallbackButton("❌ Batal", []byte("a1:myxl:cancel_wizard")),
		}}})
		msg, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			fmt.Sprintf("❌ <b>Verifikasi OTP Gagal:</b>\n<code>%s</code>\n\nPeriksa kembali kode SMS Anda atau kirim ulang OTP.", html.EscapeString(err.Error())),
			markup)
		if sendErr == nil {
			m.registerMessageInstance(userID, msg)
		}
		return true, sendErr
	}

	acc, _ := m.plugin.repo.GetByMSISDN(cCtx, sess.MSISDN)
	if acc == nil {
		acc = &Account{MSISDN: sess.MSISDN}
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

	_ = m.plugin.repo.Save(cCtx, acc)
	_ = m.plugin.repo.SetActive(cCtx, sess.MSISDN)
	m.ClearSession(userID)

	successMsg := fmt.Sprintf(
		"🎉 <b>Login Berhasil!</b>\n\n"+
			"Nomor <code>%s</code> telah tersimpan dan aktif di database.\n\n"+
			"Tekan tombol di bawah untuk membuka dashboard MyXL Anda.",
		html.EscapeString(sess.MSISDN),
	)
	markup := render.ToTelegramMarkup(ui.Markup{Rows: []ui.ButtonRow{{
		ui.NewCallbackButton("📱 Buka Dashboard MyXL", []byte("a1:myxl:home")),
	}}})
	msg, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), successMsg, markup)
	if sendErr == nil {
		m.registerMessageInstance(userID, msg)
	}
	return true, sendErr
}

func (m *MenuManager) handleWizardSetAlias(ctx context.Context, userID int64, input string, sess *wizardSession, inter interaction.MessageInteraction) (bool, error) {
	alias := strings.TrimSpace(input)
	if len(alias) > 24 {
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			"⚠️ Panjang alias maksimal 24 karakter. Silakan kirimkan nama yang lebih pendek:",
			nil)
		return true, sendErr
	}

	cCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := m.plugin.repo.SetAlias(cCtx, sess.MSISDN, alias); err != nil {
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			fmt.Sprintf("❌ Gagal menyimpan alias: %v", err), nil)
		m.ClearSession(userID)
		return true, sendErr
	}
	m.ClearSession(userID)

	msg := fmt.Sprintf("✅ <b>Alias Berhasil Diatur!</b>\n\nNomor: <code>%s</code>\nAlias: <b>%s</b>",
		html.EscapeString(sess.MSISDN), html.EscapeString(alias))
	markup := render.ToTelegramMarkup(ui.Markup{Rows: []ui.ButtonRow{{
		ui.NewCallbackButton("👥 Kelola Akun", []byte("a1:myxl:accounts")),
		ui.NewCallbackButton("📱 Dashboard", []byte("a1:myxl:home")),
	}}})
	if sess.Target.IsValid() {
		if editErr := inter.Edit(ctx, sess.Target, msg, markup); editErr == nil {
			return true, nil
		}
	}
	_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), msg, markup)
	return true, sendErr
}

func (m *MenuManager) handleWizardOptionCode(ctx context.Context, userID int64, input string, sess *wizardSession, inter interaction.MessageInteraction) (bool, error) {
	optCode := strings.TrimSpace(input)
	if optCode == "" {
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), "⚠️ Kode paket tidak boleh kosong.", nil)
		return true, sendErr
	}

	cCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	acc, err := m.plugin.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		m.ClearSession(userID)
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), "❌ Tidak ada akun MyXL aktif.", nil)
		return true, sendErr
	}

	screen, err := m.BuildPackageDetailScreen(cCtx, acc, optCode)
	if err != nil {
		m.ClearSession(userID)
		msg := fmt.Sprintf("❌ Gagal memuat paket <code>%s</code>:\n<code>%s</code>", html.EscapeString(optCode), html.EscapeString(err.Error()))
		if sess.Target.IsValid() {
			if editErr := inter.Edit(ctx, sess.Target, msg, nil); editErr == nil {
				return true, nil
			}
		}
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), msg, nil)
		return true, sendErr
	}
	m.ClearSession(userID)

	text, markup := render.ToTelegram(screen)
	if sess.Target.IsValid() {
		if editErr := inter.Edit(ctx, sess.Target, text, markup); editErr == nil {
			return true, nil
		}
	}
	_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), text, markup)
	return true, sendErr
}

func (m *MenuManager) handleWizardCustomPrice(ctx context.Context, userID int64, input string, sess *wizardSession, inter interaction.MessageInteraction) (bool, error) {
	val, err := strconv.ParseInt(strings.TrimSpace(input), 10, 64)
	if err != nil || val < 0 {
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			"⚠️ Nominal harga tidak valid. Masukkan angka bulat non-negatif (contoh: <code>0</code> atau <code>1000</code>):",
			nil)
		return true, sendErr
	}

	cCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	acc, err := m.plugin.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		m.ClearSession(userID)
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), "❌ Tidak ada akun MyXL aktif.", nil)
		return true, sendErr
	}

	details, err := m.plugin.client.GetPackageDetails(cCtx, acc, sess.OptionCode)
	if err != nil || details.TokenConfirmation == "" {
		m.ClearSession(userID)
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), "❌ Gagal memuat token konfirmasi paket.", nil)
		return true, sendErr
	}

	pkgName := sess.OptionCode
	var origPrice int64
	if details.PackageOption != nil {
		pkgName = details.PackageOption.Name
		origPrice = int64(details.PackageOption.Price)
	}

	draft := purchaseDraftState{
		MSISDN:            acc.MSISDN,
		OptionCode:        sess.OptionCode,
		PackageName:       pkgName,
		Price:             origPrice,
		TokenConfirmation: details.TokenConfirmation,
		Method:            sess.Method,
		WalletNumber:      acc.MSISDN,
		OverwritePrice:    val,
		HasOverwrite:      true,
	}
	m.ClearSession(userID)

	screen, err := m.BuildCheckoutScreen(draft, userID, sess.Target.ChatID())
	if err != nil {
		msg := fmt.Sprintf("❌ Gagal membuat sesi checkout: %v", err)
		if sess.Target.IsValid() {
			if editErr := inter.Edit(ctx, sess.Target, msg, nil); editErr == nil {
				return true, nil
			}
		}
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), msg, nil)
		return true, sendErr
	}

	text, markup := render.ToTelegram(screen)
	if sess.Target.IsValid() {
		if editErr := inter.Edit(ctx, sess.Target, text, markup); editErr == nil {
			return true, nil
		}
	}
	_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), text, markup)
	return true, sendErr
}

func (m *MenuManager) handleWizardFamilyCode(ctx context.Context, userID int64, input string, sess *wizardSession, inter interaction.MessageInteraction) (bool, error) {
	familyCode := strings.TrimSpace(input)
	if familyCode == "" {
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			"⚠️ Family Code tidak boleh kosong.\n\nContoh: <code>7658c955-a0b9-405f-bb17-de7f43d1a946</code>.\nKirimkan kode atau ketik <code>/cancel</code> untuk batal.",
			nil)
		return true, sendErr
	}

	acc, err := m.plugin.repo.GetActive(ctx)
	if err != nil || acc == nil {
		m.ClearSession(userID)
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), "❌ Tidak ada akun aktif terhubung.", nil)
		return true, sendErr
	}

	// Immediate progress feedback
	if sess.Target.IsValid() {
		_ = inter.Edit(ctx, sess.Target,
			fmt.Sprintf("⏳ <b>Mencari daftar paket...</b>\n\nFamily: <code>%s</code>\nMohon tunggu sebentar...", html.EscapeString(familyCode)),
			nil)
	}

	cCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	screen, err := m.BuildFamilyPackagesScreen(cCtx, acc, familyCode, 1)
	if err != nil {
		m.ClearSession(userID)
		msg := fmt.Sprintf("⚠️ <b>Gagal mencari paket:</b> %v\n\nPastikan Family Code benar (contoh: <code>7658c955-a0b9-405f-bb17-de7f43d1a946</code>) atau ketik <code>/cancel</code> untuk batal.", err)
		if sess.Target.IsValid() {
			if editErr := inter.Edit(ctx, sess.Target, msg, nil); editErr == nil {
				return true, nil
			}
		}
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), msg, nil)
		return true, sendErr
	}

	m.ClearSession(userID)
	text, markup := render.ToTelegram(screen)
	if sess.Target.IsValid() {
		if editErr := inter.Edit(ctx, sess.Target, text, markup); editErr == nil {
			return true, nil
		}
	}
	_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), text, markup)
	return true, sendErr
}

// ==================== SCREEN BUILDERS ====================

func (m *MenuManager) BuildDashboardScreen(ctx context.Context, mask bool) (*ui.Screen, error) {
	acc, err := m.plugin.repo.GetActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("read active account: %w", err)
	}

	if acc == nil {
		card := ui.NewCard("MyXL Control Center").
			WithIcon("📱").
			WithHeader("Kelola akun dan pantau kuota internet real-time.").
			AddField("Status Akun", "⚠️ Belum ada akun terhubung").
			WithRaw("Silakan login menggunakan nomor XL/Axis Anda. Anda akan menerima kode verifikasi OTP melalui SMS.").
			WithFooter("<i>Tekan tombol Login di bawah untuk memulai.</i>")
		screen := menu.NewScreen(menu.ScreenIDMyXL, "", card.Render())
		screen.AddRow(menu.NewButton("➕ Login Akun Baru (OTP)", "a1:myxl:login_req"))
		screen.AddRow(menu.NewButton("❌ Tutup", "a1:assistant:close"))
		return screen, nil
	}

	qCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	balance, _ := m.plugin.client.GetBalance(qCtx, acc)
	quota, _ := m.plugin.client.GetQuotaDetails(qCtx, acc)

	displayNum := acc.MSISDN
	if mask {
		displayNum = MaskMSISDN(acc.MSISDN)
	}

	card := ui.NewCard("MyXL Control Center").
		WithIcon("📱").
		WithHeader("Kelola akun dan pantau kuota internet real-time.").
		AddField("Nomor", "<code>"+displayNum+"</code>")

	if acc.Alias != "" {
		card.AddField("Alias", html.EscapeString(acc.Alias))
	}
	card.AddField("Status", "🟢 Aktif & Terhubung")

	if balance != nil {
		card.AddField("Pulsa", fmt.Sprintf("<code>Rp %s</code>", formatRupiah(int64(balance.Remaining))))
		if balance.ExpiredAt > 0 {
			card.AddField("Masa Aktif", FormatWIBTime(balance.ExpiredAt))
		}
	}

	if quota != nil && len(quota.Quotas) > 0 {
		var qb strings.Builder
		qb.WriteString("📦 <b>Ringkasan Paket:</b>\n")
		count := 0
		for _, q := range quota.Quotas {
			for _, ben := range q.Benefits {
				if strings.EqualFold(ben.DataType, "DATA") || ben.Total > 1000 {
					bar, pct := RenderProgressBar(ben.Remaining, ben.Total, 10)
					qb.WriteString(fmt.Sprintf("• <b>%s:</b>\n  <code>%s %s</code>\n  <i>%s / %s</i>\n",
						html.EscapeString(q.Name), bar, pct, FormatBytes(ben.Remaining), FormatBytes(ben.Total)))
					count++
					break
				}
			}
			if count >= 2 {
				break
			}
		}
		if count > 0 {
			card.WithRaw(qb.String())
		}
	} else {
		card.WithRaw("<i>Tidak ada kuota data aktif yang terdeteksi.</i>")
	}

	var pendingQR *PendingQRIS
	if m.plugin != nil && m.plugin.repo != nil {
		pendingQR, _ = m.plugin.repo.GetPendingQRIS(ctx, acc.MSISDN)
	}
	if pendingQR != nil {
		rem := time.Until(pendingQR.ExpiresAt).Round(time.Second)
		if rem > 0 {
			card.WithRaw(fmt.Sprintf("⏳ <b>Tagihan QRIS Menunggu Pembayaran:</b>\n• <b>Paket:</b> %s\n• <b>Nominal:</b> Rp %s\n• <b>Sisa Waktu:</b> %s (s/d %s)\n",
				html.EscapeString(pendingQR.PackageName), formatRupiah(pendingQR.Price), FormatRemainingDuration(rem), FormatWIBClock(pendingQR.ExpiresAt)))
		}
	}

	card.WithFooter("<i>Pilih menu di bawah untuk rincian kuota, akun, atau belanja paket.</i>")
	screen := menu.NewScreen(menu.ScreenIDMyXL, "", card.Render())
	if pendingQR != nil && time.Now().UTC().Before(pendingQR.ExpiresAt) {
		rem := time.Until(pendingQR.ExpiresAt).Round(time.Second)
		screen.AddRow(
			menu.NewButton("📱 Lihat QRIS Aktif ("+FormatRemainingDuration(rem)+")", "a1:myxl:pending_qris"),
		)
	}
	screen.AddRow(
		menu.NewButton("🔄 Perbarui Kuota", "a1:myxl:refresh"),
		menu.NewButton("📊 Rincian Kuota", "a1:myxl:detail"),
	)
	screen.AddRow(
		menu.NewButton("👥 Kelola Akun", "a1:myxl:accounts"),
		menu.NewButton("🛒 Beli Paket", "a1:myxl:store"),
	)
	screen.AddRow(
		menu.NewButton("⭐ Paket Favorit", "a1:myxl:saved"),
		menu.NewButton("❌ Tutup Menu", "a1:assistant:close"),
	)
	return screen, nil
}

func (m *MenuManager) BuildQuotaDetailScreen(ctx context.Context, mask bool) (*ui.Screen, error) {
	acc, err := m.plugin.repo.GetActive(ctx)
	if err != nil || acc == nil {
		return nil, fmt.Errorf("no active account")
	}

	qCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	balance, _ := m.plugin.client.GetBalance(qCtx, acc)
	quota, _ := m.plugin.client.GetQuotaDetails(qCtx, acc)

	formatted := FormatQuotaResponse(acc, balance, quota, mask)
	screen := menu.NewScreen("myxl:detail", "", formatted)
	screen.AddRow(
		menu.NewButton("🔄 Perbarui", "a1:myxl:detail"),
		menu.NewButton("🔙 Kembali ke MyXL", "a1:myxl:home"),
	)
	return screen, nil
}

func (m *MenuManager) BuildAccountsScreen(ctx context.Context) (*ui.Screen, error) {
	accounts, err := m.plugin.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}

	card := ui.NewCard("Kelola Akun MyXL").
		WithIcon("👥").
		WithHeader("Pilih akun aktif atau kelola profil nomor tersimpan.")

	if len(accounts) == 0 {
		card.WithRaw("<i>Belum ada akun MyXL yang tersimpan.</i>")
		screen := menu.NewScreen("myxl:accounts", "", card.Render())
		screen.AddRow(menu.NewButton("➕ Tambah Akun", "a1:myxl:login_req"))
		screen.AddRow(menu.NewButton("🔙 Kembali ke MyXL", "a1:myxl:home"))
		return screen, nil
	}

	var sb strings.Builder
	sb.WriteString("<b>Daftar Akun Tersimpan:</b>\n")
	for i, acc := range accounts {
		badge := "⚪"
		statusText := ""
		if acc.IsActive {
			badge = "🟢"
			statusText = " — <b>[Aktif]</b>"
		}
		aliasText := ""
		if acc.Alias != "" {
			aliasText = fmt.Sprintf(" (%s)", html.EscapeString(acc.Alias))
		}
		sb.WriteString(fmt.Sprintf("%s %d. <code>%s</code>%s%s\n", badge, i+1, html.EscapeString(acc.MSISDN), aliasText, statusText))
		if !acc.TokenExpiresAt.IsZero() {
			sb.WriteString(fmt.Sprintf("   <i>Token Exp: %s</i>\n", acc.TokenExpiresAt.Format("2006-01-02 15:04")))
		}
	}
	card.WithRaw(sb.String())
	card.WithFooter("<i>Ketuk nomor di bawah untuk mengganti akun aktif secara instan.</i>")

	screen := menu.NewScreen("myxl:accounts", "", card.Render())

	// Grid buttons to switch accounts
	var switchRow ui.ButtonRow
	for _, acc := range accounts {
		label := acc.MSISDN
		if acc.Alias != "" {
			label = acc.Alias
		}
		if acc.IsActive {
			switchRow = append(switchRow, menu.NewButton("🟢 "+truncateString(label, 12), "a1:myxl:noop"))
		} else {
			switchRow = append(switchRow, menu.NewButton("👉 "+truncateString(label, 12), fmt.Sprintf("a1:myxl:switch:%s", acc.MSISDN)))
		}
		if len(switchRow) == 2 {
			screen.AddRow(switchRow...)
			switchRow = nil
		}
	}
	if len(switchRow) > 0 {
		screen.AddRow(switchRow...)
	}

	screen.AddRow(
		menu.NewButton("➕ Tambah Akun", "a1:myxl:login_req"),
		menu.NewButton("🏷️ Ubah Alias", "a1:myxl:alias_pick"),
	)
	screen.AddRow(
		menu.NewButton("🗑️ Hapus Akun", "a1:myxl:del_pick"),
		menu.NewButton("🔄 Refresh Token", "a1:myxl:token_refresh"),
	)
	screen.AddRow(
		menu.NewButton("🔙 Kembali ke MyXL", "a1:myxl:home"),
	)
	return screen, nil
}

func (m *MenuManager) BuildStoreScreen(ctx context.Context) (*ui.Screen, error) {
	acc, err := m.plugin.repo.GetActive(ctx)
	if err != nil || acc == nil {
		return nil, fmt.Errorf("no active account")
	}

	card := ui.NewCard("Beli Paket MyXL").
		WithIcon("🛒").
		WithHeader("Pilih metode pencarian paket yang ingin Anda beli.").
		AddField("Akun Aktif", "<code>"+MaskMSISDN(acc.MSISDN)+"</code>")

	bCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if bal, bErr := m.plugin.client.GetBalance(bCtx, acc); bErr == nil && bal != nil {
		card.AddField("Sisa Pulsa", fmt.Sprintf("Rp %s", formatRupiah(int64(bal.Remaining))))
	}

	card.WithRaw(
		"• <b>Paket Favorit:</b> Akses cepat paket yang sudah Anda simpan.\n" +
			"• <b>Family Code:</b> Cari paket berdasarkan ID grup paket (contoh: <code>7658c955-a0b9-405f-bb17-de7f43d1a946</code>).\n" +
			"• <b>Input Option Code:</b> Masukkan Option Code secara langsung (misal: <code>OPT12345</code>).\n",
	)
	card.WithFooter("<i>Pilih salah satu metode di bawah.</i>")

	screen := menu.NewScreen("myxl:store", "", card.Render())
	screen.AddRow(menu.NewButton("⭐ Paket Favorit Tersimpan", "a1:myxl:saved"))
	screen.AddRow(menu.NewButton("🔍 Cari dari Family Code", "a1:myxl:fam_input"))
	screen.AddRow(menu.NewButton("⚡ Masukkan Option Code", "a1:myxl:buy_opt_input"))
	screen.AddRow(menu.NewButton("🔙 Kembali ke MyXL", "a1:myxl:home"))
	return screen, nil
}

func (m *MenuManager) BuildSavedPackagesScreen(ctx context.Context) (*ui.Screen, error) {
	acc, err := m.plugin.repo.GetActive(ctx)
	if err != nil || acc == nil {
		return nil, fmt.Errorf("no active account")
	}

	saved, err := m.plugin.repo.GetSavedPackages(ctx, acc.MSISDN)
	if err != nil {
		return nil, fmt.Errorf("get saved packages: %w", err)
	}

	card := ui.NewCard("Paket Favorit MyXL").
		WithIcon("⭐").
		WithHeader("Daftar paket yang Anda simpan untuk pembelian cepat.")

	if len(saved) == 0 {
		card.WithRaw("<i>Belum ada paket yang disimpan dalam daftar favorit.</i>\n\nAnda dapat menyimpan paket ke favorit setelah melihat rincian paket atau menyelesaikan transaksi.")
		screen := menu.NewScreen("myxl:saved", "", card.Render())
		screen.AddRow(menu.NewButton("⚡ Masukkan Option Code", "a1:myxl:buy_opt_input"))
		screen.AddRow(menu.NewButton("🔙 Kembali ke Store", "a1:myxl:store"))
		return screen, nil
	}

	var sb strings.Builder
	sb.WriteString("<b>Paket Tersimpan:</b>\n")
	for i, sp := range saved {
		sb.WriteString(fmt.Sprintf("%d. <b>%s</b>\n   Kode: <code>%s</code> — Rp %s\n",
			i+1, html.EscapeString(sp.Name), html.EscapeString(sp.OptionCode), formatRupiah(sp.Price)))
	}
	card.WithRaw(sb.String())

	screen := menu.NewScreen("myxl:saved", "", card.Render())
	for _, sp := range saved {
		label := truncateString(sp.Name, 18)
		optKey := m.RegisterOptionCode(sp.OptionCode)
		screen.AddRow(
			menu.NewButton("🛒 "+label, fmt.Sprintf("a1:myxl:buy_opt:%s", optKey)),
			menu.NewButton("❌ Hapus", fmt.Sprintf("a1:myxl:bookmark_del:%s", optKey)),
		)
	}
	screen.AddRow(menu.NewButton("🔙 Kembali ke Store", "a1:myxl:store"))
	return screen, nil
}

func (m *MenuManager) BuildPackageDetailScreen(ctx context.Context, acc *Account, optionCode string) (*ui.Screen, error) {
	details, err := m.plugin.client.GetPackageDetails(ctx, acc, optionCode)
	if err != nil {
		return nil, err
	}
	if details.TokenConfirmation == "" {
		return nil, fmt.Errorf("token konfirmasi tidak ditemukan untuk paket %s", optionCode)
	}

	pkgName := optionCode
	var price int64
	if details.PackageOption != nil {
		pkgName = details.PackageOption.Name
		price = int64(details.PackageOption.Price)
	}

	card := ui.NewCard("Detail Paket MyXL").
		WithIcon("📦").
		WithHeader("Periksa paket dan pilih metode pembayaran.").
		AddField("Paket", html.EscapeString(pkgName)).
		AddField("Option Code", "<code>"+html.EscapeString(optionCode)+"</code>").
		AddField("Harga Resmi", fmt.Sprintf("Rp %s", formatRupiah(price))).
		AddField("Token Status", "✅ Tersedia & Siap Transaksi")

	card.WithFooter("<i>Pilih salah satu metode pembayaran di bawah untuk melanjutkan.</i>")

	optKey := m.RegisterOptionCode(optionCode)

	screen := menu.NewScreen("myxl:pkg_detail", "", card.Render())
	screen.AddRow(
		menu.NewButton("💰 Pulsa", fmt.Sprintf("a1:myxl:method:balance:%s", optKey)),
		menu.NewButton("📱 QRIS", fmt.Sprintf("a1:myxl:method:qris:%s", optKey)),
	)
	screen.AddRow(
		menu.NewButton("🟢 GoPay", fmt.Sprintf("a1:myxl:method:gopay:%s", optKey)),
		menu.NewButton("🟣 OVO", fmt.Sprintf("a1:myxl:method:ovo:%s", optKey)),
	)
	screen.AddRow(
		menu.NewButton("🔵 DANA", fmt.Sprintf("a1:myxl:method:dana:%s", optKey)),
		menu.NewButton("🟠 ShopeePay", fmt.Sprintf("a1:myxl:method:shopeepay:%s", optKey)),
	)
	screen.AddRow(
		menu.NewButton("⚡ Decoy Pulsa", fmt.Sprintf("a1:myxl:method:decoy_balance:%s", optKey)),
		menu.NewButton("⚡ Decoy QRIS", fmt.Sprintf("a1:myxl:method:decoy_qris:%s", optKey)),
	)
	screen.AddRow(
		menu.NewButton("✏️ Overwrite Harga", fmt.Sprintf("a1:myxl:custom_price:%s", optKey)),
		menu.NewButton("⭐ Simpan Favorit", fmt.Sprintf("a1:myxl:bookmark_add:%s", optKey)),
	)
	screen.AddRow(
		menu.NewButton("🔙 Batal / Kembali", "a1:myxl:store"),
	)
	return screen, nil
}

func (m *MenuManager) BuildCheckoutScreen(draft purchaseDraftState, userID, chatID int64) (*ui.Screen, error) {
	if m.plugin.stateStore == nil {
		return nil, fmt.Errorf("state store unavailable")
	}

	oid := m.plugin.stateStore.StoreWithScope(draft, coreCallback.StateScope{
		UserID:    userID,
		ChatID:    chatID,
		Namespace: m.plugin.Namespace(),
		SingleUse: true,
	}, 5*time.Minute)
	if oid == "" {
		return nil, fmt.Errorf("failed to create checkout session")
	}

	effectivePrice := draft.Price
	priceLabel := fmt.Sprintf("Rp %s", formatRupiah(effectivePrice))
	if draft.HasOverwrite {
		effectivePrice = draft.OverwritePrice
		priceLabel = fmt.Sprintf("Rp %s <i>(Harga Overwrite)</i>", formatRupiah(effectivePrice))
	}

	card := ui.NewCard("Konfirmasi Pembelian MyXL").
		WithIcon("⚠️").
		WithHeader("Harap periksa rincian pembelian Anda sebelum melanjutkan.").
		AddField("Paket", html.EscapeString(draft.PackageName)).
		AddField("Option Code", "<code>"+html.EscapeString(draft.OptionCode)+"</code>").
		AddField("Metode", "<code>"+strings.ToUpper(html.EscapeString(draft.Method))+"</code>").
		AddField("Total Bayar", priceLabel).
		AddField("Target Nomor", "<code>"+html.EscapeString(draft.MSISDN)+"</code>").
		WithRaw("<i>Proteksi idempotensi aktif. Transaksi ini hanya akan dieksekusi 1 kali dan tidak dapat dibatalkan setelah dikonfirmasi.</i>").
		WithFooter("<i>Tekan Konfirmasi Pembayaran untuk menjalankan transaksi.</i>")

	screen := menu.NewScreen("myxl:checkout", "", card.Render())
	screen.AddRow(
		menu.NewButton("✅ Konfirmasi Pembayaran", fmt.Sprintf("a1:myxl:checkout:%s", oid)),
		menu.NewButton("❌ Batal", fmt.Sprintf("a1:myxl:cancel_draft:%s", oid)),
	)
	return screen, nil
}

func (m *MenuManager) BuildPurchaseResultScreen(result *SettlementResult, packageName string, effectivePrice int64, method, optionCode string) *ui.Screen {
	title := "Pembelian Gagal"
	icon := "❌"
	if result != nil && result.IsSuccess {
		title = "Pembelian Berhasil!"
		icon = "🎉"
	}

	card := ui.NewCard(title).
		WithIcon(icon).
		AddField("Paket", html.EscapeString(packageName)).
		AddField("Metode", strings.ToUpper(html.EscapeString(method))).
		AddField("Nominal", fmt.Sprintf("Rp %s", formatRupiah(effectivePrice)))

	if result != nil {
		if result.TransactionCode != "" {
			card.AddField("Kode Transaksi", "<code>"+html.EscapeString(result.TransactionCode)+"</code>")
		}
		if result.Message != "" {
			card.AddField("Pesan Operator", html.EscapeString(result.Message))
		}
		if result.QRCode != "" {
			wibLoc := time.FixedZone("WIB", 7*3600)
			expireWIB := time.Now().UTC().Add(5 * time.Minute).In(wibLoc).Format("15:04:05")
			preview, truncated := inlineQRPreview(result.QRCode)
			card.AddField("Batas Waktu", fmt.Sprintf("5 Menit (s/d %s WIB)", expireWIB))
			note := "<i>💡 Foto QRIS dikirimkan di bawah ini. QRIS berlaku 5 menit dan dapat dilihat kembali di Dashboard atau perintah <code>.myxl qris</code> selama belum dibayar.</i>"
			if truncated {
				note = "<i>💡 String dipersingkat agar aman untuk Telegram; payload penuh tetap tersedia pada foto QRIS.</i>"
			}
			card.WithRaw("📱 <b>Kode / String QRIS:</b>\n<code>" + html.EscapeString(preview) + "</code>\n\n" + note)
		}
	}

	screen := menu.NewScreen("myxl:result", "", card.Render())
	optKey := m.RegisterOptionCode(optionCode)
	var firstRow []ui.Button
	firstRow = append(firstRow, menu.NewButton("⭐ Simpan ke Favorit", fmt.Sprintf("a1:myxl:bookmark_add:%s", optKey)))
	if result != nil && result.QRCode != "" {
		qrKey := m.RegisterQR(result.QRCode)
		if qrKey != "" {
			firstRow = append(firstRow, menu.NewButton("🖼️ Kirim Foto QRIS", fmt.Sprintf("a1:myxl:qris_img:%s", qrKey)))
		}
	}
	screen.AddRow(firstRow...)
	screen.AddRow(
		menu.NewButton("📱 Buka Dashboard", "a1:myxl:home"),
	)
	return screen
}

// BuildPendingQRISScreen renders the screen for active unexpired pending QRIS.
func (m *MenuManager) BuildPendingQRISScreen(ctx context.Context) (*ui.Screen, error) {
	acc, err := m.plugin.repo.GetActive(ctx)
	if err != nil || acc == nil {
		return nil, fmt.Errorf("no active account")
	}

	pending, err := m.plugin.repo.GetPendingQRIS(ctx, acc.MSISDN)
	if err != nil || pending == nil {
		card := ui.NewCard("Tagihan QRIS").
			WithIcon("ℹ️").
			WithRaw("<i>Tidak ada transaksi QRIS aktif yang menunggu pembayaran.\nTransaksi QRIS otomatis kedaluwarsa setelah 5 menit.</i>")
		screen := menu.NewScreen("myxl:pending_qris", "", card.Render())
		screen.AddRow(menu.NewButton("🔙 Kembali ke Dashboard", "a1:myxl:home"))
		return screen, nil
	}

	rem := time.Until(pending.ExpiresAt).Round(time.Second)
	remStr := FormatRemainingDuration(rem)
	expireWIB := FormatWIBClock(pending.ExpiresAt)

	card := ui.NewCard("Tagihan QRIS Menunggu Pembayaran").
		WithIcon("⏳").
		AddField("Paket", html.EscapeString(pending.PackageName)).
		AddField("Nominal", fmt.Sprintf("Rp %s", formatRupiah(pending.Price))).
		AddField("Batas Waktu", fmt.Sprintf("%s (Sisa: %s)", expireWIB, remStr))

	if pending.TransactionCode != "" {
		card.AddField("Kode Transaksi", "<code>"+html.EscapeString(pending.TransactionCode)+"</code>")
	}

	preview, truncated := inlineQRPreview(pending.QRCode)
	note := "<i>💡 Foto QRIS dikirimkan ke chat. Anda dapat scan langsung atau upload dari galeri aplikasi e-wallet / mobile banking.</i>"
	if truncated {
		note = "<i>💡 String dipersingkat agar aman untuk Telegram; payload penuh tetap tersedia pada foto QRIS.</i>"
	}
	card.WithRaw("📱 <b>Kode / String QRIS:</b>\n<code>" + html.EscapeString(preview) + "</code>\n\n" + note)

	screen := menu.NewScreen("myxl:pending_qris", "", card.Render())
	qrKey := m.RegisterQR(pending.QRCode)
	var actionRow []ui.Button
	if qrKey != "" {
		actionRow = append(actionRow, menu.NewButton("🖼️ Kirim Foto QRIS", fmt.Sprintf("a1:myxl:qris_img:%s", qrKey)))
	}
	actionRow = append(actionRow, menu.NewButton("🗑️ Batalkan", fmt.Sprintf("a1:myxl:qris_cancel:%s", pending.TransactionCode)))
	screen.AddRow(actionRow...)
	screen.AddRow(
		menu.NewButton("🔄 Cek Status", "a1:myxl:pending_qris"),
		menu.NewButton("🔙 Kembali ke Dashboard", "a1:myxl:home"),
	)
	return screen, nil
}

// BuildDeletePickScreen lets user choose which account to delete.
func (m *MenuManager) BuildDeletePickScreen(ctx context.Context) (*ui.Screen, error) {
	accounts, err := m.plugin.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	card := ui.NewCard("Hapus Akun MyXL").
		WithIcon("🗑️").
		WithHeader("Pilih akun yang ingin Anda hapus dari penyimpanan:")
	screen := menu.NewScreen("myxl:del_pick", "", card.Render())
	for _, acc := range accounts {
		label := acc.MSISDN
		if acc.Alias != "" {
			label = fmt.Sprintf("%s (%s)", acc.MSISDN, acc.Alias)
		}
		screen.AddRow(
			menu.NewButton("🗑️ "+truncateString(label, 20), fmt.Sprintf("a1:myxl:del_ask:%s", acc.MSISDN)),
		)
	}
	screen.AddRow(menu.NewButton("🔙 Batal", "a1:myxl:accounts"))
	return screen, nil
}

// BuildAliasPickScreen lets user choose which account to change alias.
func (m *MenuManager) BuildAliasPickScreen(ctx context.Context) (*ui.Screen, error) {
	accounts, err := m.plugin.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	card := ui.NewCard("Ubah Alias Akun").
		WithIcon("🏷️").
		WithHeader("Pilih akun yang ingin Anda ubah namanya:")
	screen := menu.NewScreen("myxl:alias_pick", "", card.Render())
	for _, acc := range accounts {
		label := acc.MSISDN
		if acc.Alias != "" {
			label = fmt.Sprintf("%s (%s)", acc.MSISDN, acc.Alias)
		}
		screen.AddRow(
			menu.NewButton("🏷️ "+truncateString(label, 20), fmt.Sprintf("a1:myxl:alias_req:%s", acc.MSISDN)),
		)
	}
	screen.AddRow(menu.NewButton("🔙 Batal", "a1:myxl:accounts"))
	return screen, nil
}

// familyOptionItem holds a flattened option with its parent variant name.
type familyOptionItem struct {
	VariantName string
	Option      PackageOption
}

// BuildFamilyPackagesScreen renders a paginated list of packages belonging to a family code,
// with numbered selection buttons and Prev/Next pagination controls.
func (m *MenuManager) BuildFamilyPackagesScreen(ctx context.Context, acc *Account, familyCode string, page int) (*ui.Screen, error) {
	if acc == nil {
		return nil, fmt.Errorf("no active account")
	}

	res, err := m.plugin.client.GetPackagesByFamily(ctx, acc, familyCode)
	if err != nil {
		return nil, fmt.Errorf("lookup family: %w", err)
	}
	if res == nil || len(res.PackageVariants) == 0 {
		return nil, fmt.Errorf("tidak ada paket ditemukan untuk family '%s'", familyCode)
	}

	var allOptions []familyOptionItem
	for _, v := range res.PackageVariants {
		for _, opt := range v.PackageOptions {
			allOptions = append(allOptions, familyOptionItem{
				VariantName: v.Name,
				Option:      opt,
			})
		}
	}

	if len(allOptions) == 0 {
		return nil, fmt.Errorf("tidak ada opsi paket tersedia dalam family '%s'", familyCode)
	}

	const pageSize = 5
	totalItems := len(allOptions)
	totalPages := (totalItems + pageSize - 1) / pageSize
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	startIdx := (page - 1) * pageSize
	endIdx := startIdx + pageSize
	if endIdx > totalItems {
		endIdx = totalItems
	}
	pageItems := allOptions[startIdx:endIdx]

	famTitle := res.PackageFamily.Name
	if famTitle == "" {
		famTitle = familyCode
	}

	card := ui.NewCard(fmt.Sprintf("Paket %s", famTitle)).
		WithIcon("📦").
		WithHeader(fmt.Sprintf("Daftar paket untuk Family <code>%s</code>", html.EscapeString(familyCode))).
		AddField("Halaman", fmt.Sprintf("%d dari %d (Total %d paket)", page, totalPages, totalItems))

	var listBuf strings.Builder
	for i, item := range pageItems {
		globalNum := startIdx + i + 1
		priceStr := formatRupiah(int64(item.Option.Price))
		listBuf.WriteString(fmt.Sprintf("<b>[%d] %s</b>\n", globalNum, html.EscapeString(item.Option.Name)))
		if item.VariantName != "" && item.VariantName != item.Option.Name {
			listBuf.WriteString(fmt.Sprintf("    <i>Varian: %s</i>\n", html.EscapeString(item.VariantName)))
		}
		listBuf.WriteString(fmt.Sprintf("    Harga: <code>Rp %s</code>\n", priceStr))
		listBuf.WriteString(fmt.Sprintf("    Kode: <code>%s</code>\n\n", html.EscapeString(item.Option.PackageOptionCode)))
	}
	listBuf.WriteString("<i>Pilih nomor paket di bawah untuk melihat rincian & checkout.</i>")
	card.WithRaw(listBuf.String())

	screen := menu.NewScreen("myxl:fam_list", "", card.Render())

	// 1. Number selection buttons row: [ 1 ] [ 2 ] [ 3 ] [ 4 ] [ 5 ]
	var numRow []menu.Button
	for i, item := range pageItems {
		globalNum := startIdx + i + 1
		label := fmt.Sprintf("%d", globalNum)
		optKey := m.RegisterOptionCode(item.Option.PackageOptionCode)
		numRow = append(numRow, menu.NewButton(label, fmt.Sprintf("a1:myxl:buy_opt:%s", optKey)))
	}
	if len(numRow) > 0 {
		screen.AddRow(numRow...)
	}

	// 2. Pagination row: [◀️ Prev] [📄 X/Y] [▶️ Next]
	var navRow []menu.Button
	if page > 1 {
		navRow = append(navRow, menu.NewButton("◀️ Prev", fmt.Sprintf("a1:myxl:fam_page:%s:%d", familyCode, page-1)))
	} else {
		navRow = append(navRow, menu.NewButton("⏮️", "a1:myxl:noop"))
	}
	navRow = append(navRow, menu.NewButton(fmt.Sprintf("📄 %d/%d", page, totalPages), "a1:myxl:noop"))
	if page < totalPages {
		navRow = append(navRow, menu.NewButton("▶️ Next", fmt.Sprintf("a1:myxl:fam_page:%s:%d", familyCode, page+1)))
	} else {
		navRow = append(navRow, menu.NewButton("⏭️", "a1:myxl:noop"))
	}
	screen.AddRow(navRow...)

	// 3. Back button
	screen.AddRow(menu.NewButton("🔙 Kembali ke Store", "a1:myxl:store"))

	return screen, nil
}
