package myxl

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

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
	_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), prompt, markup)
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
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			fmt.Sprintf("❌ <b>Verifikasi OTP Gagal:</b>\n<code>%s</code>\n\nPeriksa kembali kode SMS Anda atau kirim ulang OTP.", html.EscapeString(err.Error())),
			markup)
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
	_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), successMsg, markup)
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
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(),
			fmt.Sprintf("❌ Gagal memuat paket <code>%s</code>:\n<code>%s</code>", html.EscapeString(optCode), html.EscapeString(err.Error())),
			nil)
		m.ClearSession(userID)
		return true, sendErr
	}
	m.ClearSession(userID)

	text, markup := render.ToTelegram(screen)
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
		_, sendErr := inter.SendMessage(ctx, sess.Target.Peer(), fmt.Sprintf("❌ Gagal membuat sesi checkout: %v", err), nil)
		return true, sendErr
	}

	text, markup := render.ToTelegram(screen)
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
		screen.AddRow(menu.NewButton("🏠 Menu Utama", "a1:assistant:start"), menu.NewButton("❌ Tutup", "a1:assistant:close"))
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

	card.WithFooter("<i>Pilih menu di bawah untuk rincian kuota, akun, atau belanja paket.</i>")
	screen := menu.NewScreen(menu.ScreenIDMyXL, "", card.Render())
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
		menu.NewButton("🏠 Menu Utama", "a1:assistant:start"),
	)
	screen.AddRow(
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
			"• <b>Input Option Code:</b> Masukkan Option Code secara langsung (misal: <code>OPT12345</code>).\n",
	)
	card.WithFooter("<i>Pilih salah satu metode di bawah.</i>")

	screen := menu.NewScreen("myxl:store", "", card.Render())
	screen.AddRow(menu.NewButton("⭐ Paket Favorit Tersimpan", "a1:myxl:saved"))
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
		screen.AddRow(
			menu.NewButton("🛒 "+label, fmt.Sprintf("a1:myxl:buy_opt:%s", sp.OptionCode)),
			menu.NewButton("❌ Hapus", fmt.Sprintf("a1:myxl:bookmark_del:%s", sp.OptionCode)),
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

	screen := menu.NewScreen("myxl:pkg_detail", "", card.Render())
	screen.AddRow(
		menu.NewButton("💰 Pulsa", fmt.Sprintf("a1:myxl:method:balance:%s", optionCode)),
		menu.NewButton("📱 QRIS", fmt.Sprintf("a1:myxl:method:qris:%s", optionCode)),
	)
	screen.AddRow(
		menu.NewButton("🟢 GoPay", fmt.Sprintf("a1:myxl:method:gopay:%s", optionCode)),
		menu.NewButton("🟣 OVO", fmt.Sprintf("a1:myxl:method:ovo:%s", optionCode)),
	)
	screen.AddRow(
		menu.NewButton("🔵 DANA", fmt.Sprintf("a1:myxl:method:dana:%s", optionCode)),
		menu.NewButton("🟠 ShopeePay", fmt.Sprintf("a1:myxl:method:shopeepay:%s", optionCode)),
	)
	screen.AddRow(
		menu.NewButton("⚡ Decoy Pulsa", fmt.Sprintf("a1:myxl:method:decoy_balance:%s", optionCode)),
		menu.NewButton("⚡ Decoy QRIS", fmt.Sprintf("a1:myxl:method:decoy_qris:%s", optionCode)),
	)
	screen.AddRow(
		menu.NewButton("✏️ Overwrite Harga", fmt.Sprintf("a1:myxl:custom_price:%s", optionCode)),
		menu.NewButton("⭐ Simpan Favorit", fmt.Sprintf("a1:myxl:bookmark_add:%s", optionCode)),
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
			card.WithRaw("📱 <b>String QRIS:</b>\n<code>" + html.EscapeString(result.QRCode) + "</code>")
		}
	}

	screen := menu.NewScreen("myxl:result", "", card.Render())
	screen.AddRow(
		menu.NewButton("⭐ Simpan ke Favorit", fmt.Sprintf("a1:myxl:bookmark_add:%s", optionCode)),
		menu.NewButton("📱 Buka Dashboard", "a1:myxl:home"),
	)
	return screen
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
