package myxl

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/ui"
)

type MenuManager struct {
	plugin *Plugin
}

func NewMenuManager(p *Plugin) *MenuManager {
	return &MenuManager{plugin: p}
}

// RegisterOptionCode keeps the canonical option code in the a2 session-owned intent.
// It no longer shortens values into the legacy callback StateStore because the intent
// itself never crosses Telegram's 64-byte callback-data boundary.
func (m *MenuManager) RegisterOptionCode(optCode string) string {
	return strings.TrimSpace(optCode)
}

func (m *MenuManager) ResolveOptionCode(keyOrCode string) string {
	return strings.TrimSpace(keyOrCode)
}

func (m *MenuManager) RegisterQR(qrPayload string) string {
	qrPayload, err := normalizeQRPayload(qrPayload)
	if err != nil {
		return ""
	}
	return qrPayload
}

func (m *MenuManager) ResolveQR(key string) string {
	return strings.TrimSpace(key)
}

// newMenuButton stores only a plugin-internal action intent. Assistant a2
// compilation replaces it with an opaque session-bound callback token.
func newMenuButton(text, data string) ui.Button {
	return ui.NewCallbackButton(text, []byte(data))
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
		screen := ui.NewScreen("myxl", "", card.Render())
		screen.AddRow(newMenuButton("➕ Login Akun Baru (OTP)", "myxl:login_req"))
		screen.AddRow(newMenuButton("❌ Tutup", "assistant:close"))
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
	screen := ui.NewScreen("myxl", "", card.Render())
	if pendingQR != nil && time.Now().UTC().Before(pendingQR.ExpiresAt) {
		rem := time.Until(pendingQR.ExpiresAt).Round(time.Second)
		screen.AddRow(
			newMenuButton("📱 Lihat QRIS Aktif ("+FormatRemainingDuration(rem)+")", "myxl:pending_qris"),
		)
	}
	screen.AddRow(
		newMenuButton("🔄 Perbarui Kuota", "myxl:refresh"),
		newMenuButton("📊 Rincian Kuota", "myxl:detail"),
	)
	screen.AddRow(
		newMenuButton("👥 Kelola Akun", "myxl:accounts"),
		newMenuButton("🛒 Beli Paket", "myxl:store"),
	)
	screen.AddRow(
		newMenuButton("⭐ Paket Favorit", "myxl:saved"),
		newMenuButton("❌ Tutup Menu", "assistant:close"),
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
	screen := ui.NewScreen("myxl:detail", "", formatted)
	screen.AddRow(
		newMenuButton("🔄 Perbarui", "myxl:detail"),
		newMenuButton("🔙 Kembali ke MyXL", "myxl:home"),
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
		screen := ui.NewScreen("myxl:accounts", "", card.Render())
		screen.AddRow(newMenuButton("➕ Tambah Akun", "myxl:login_req"))
		screen.AddRow(newMenuButton("🔙 Kembali ke MyXL", "myxl:home"))
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

	screen := ui.NewScreen("myxl:accounts", "", card.Render())

	// Grid buttons to switch accounts
	var switchRow ui.ButtonRow
	for _, acc := range accounts {
		label := acc.MSISDN
		if acc.Alias != "" {
			label = acc.Alias
		}
		if acc.IsActive {
			switchRow = append(switchRow, newMenuButton("🟢 "+truncateString(label, 12), "myxl:noop"))
		} else {
			switchRow = append(switchRow, newMenuButton("👉 "+truncateString(label, 12), fmt.Sprintf("myxl:switch:%s", acc.MSISDN)))
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
		newMenuButton("➕ Tambah Akun", "myxl:login_req"),
		newMenuButton("🏷️ Ubah Alias", "myxl:alias_pick"),
	)
	screen.AddRow(
		newMenuButton("🗑️ Hapus Akun", "myxl:del_pick"),
		newMenuButton("🔄 Refresh Token", "myxl:token_refresh"),
	)
	screen.AddRow(
		newMenuButton("🔙 Kembali ke MyXL", "myxl:home"),
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

	screen := ui.NewScreen("myxl:store", "", card.Render())
	screen.AddRow(newMenuButton("⭐ Paket Favorit Tersimpan", "myxl:saved"))
	screen.AddRow(newMenuButton("🔍 Cari dari Family Code", "myxl:fam_input"))
	screen.AddRow(newMenuButton("⚡ Masukkan Option Code", "myxl:buy_opt_input"))
	screen.AddRow(newMenuButton("🔙 Kembali ke MyXL", "myxl:home"))
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
		screen := ui.NewScreen("myxl:saved", "", card.Render())
		screen.AddRow(newMenuButton("⚡ Masukkan Option Code", "myxl:buy_opt_input"))
		screen.AddRow(newMenuButton("🔙 Kembali ke Store", "myxl:store"))
		return screen, nil
	}

	var sb strings.Builder
	sb.WriteString("<b>Paket Tersimpan:</b>\n")
	for i, sp := range saved {
		sb.WriteString(fmt.Sprintf("%d. <b>%s</b>\n   Kode: <code>%s</code> — Rp %s\n",
			i+1, html.EscapeString(sp.Name), html.EscapeString(sp.OptionCode), formatRupiah(sp.Price)))
	}
	card.WithRaw(sb.String())

	screen := ui.NewScreen("myxl:saved", "", card.Render())
	for _, sp := range saved {
		label := truncateString(sp.Name, 18)
		optKey := m.RegisterOptionCode(sp.OptionCode)
		screen.AddRow(
			newMenuButton("🛒 "+label, fmt.Sprintf("myxl:buy_opt:%s", optKey)),
			newMenuButton("❌ Hapus", fmt.Sprintf("myxl:bookmark_del:%s", optKey)),
		)
	}
	screen.AddRow(newMenuButton("🔙 Kembali ke Store", "myxl:store"))
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

	screen := ui.NewScreen("myxl:pkg_detail", "", card.Render())
	screen.AddRow(
		newMenuButton("💰 Pulsa", fmt.Sprintf("myxl:method:balance:%s", optKey)),
		newMenuButton("📱 QRIS", fmt.Sprintf("myxl:method:qris:%s", optKey)),
	)
	screen.AddRow(
		newMenuButton("🟢 GoPay", fmt.Sprintf("myxl:method:gopay:%s", optKey)),
		newMenuButton("🟣 OVO", fmt.Sprintf("myxl:method:ovo:%s", optKey)),
	)
	screen.AddRow(
		newMenuButton("🔵 DANA", fmt.Sprintf("myxl:method:dana:%s", optKey)),
		newMenuButton("🟠 ShopeePay", fmt.Sprintf("myxl:method:shopeepay:%s", optKey)),
	)
	screen.AddRow(
		newMenuButton("⚡ Decoy Pulsa", fmt.Sprintf("myxl:method:decoy_balance:%s", optKey)),
		newMenuButton("⚡ Decoy QRIS", fmt.Sprintf("myxl:method:decoy_qris:%s", optKey)),
	)
	screen.AddRow(
		newMenuButton("✏️ Overwrite Harga", fmt.Sprintf("myxl:custom_price:%s", optKey)),
		newMenuButton("⭐ Simpan Favorit", fmt.Sprintf("myxl:bookmark_add:%s", optKey)),
	)
	screen.AddRow(
		newMenuButton("🔙 Batal / Kembali", "myxl:store"),
	)
	return screen, nil
}

func (m *MenuManager) BuildCheckoutScreen(quote purchaseCheckoutPreview) (*ui.Screen, error) {
	priceLabel := fmt.Sprintf("Rp %s", formatRupiah(quote.EffectivePrice))
	if quote.Intent.HasOverwrite {
		priceLabel = fmt.Sprintf("Rp %s <i>(Harga Overwrite)</i>", formatRupiah(quote.EffectivePrice))
	}

	card := ui.NewCard("Konfirmasi Pembelian MyXL").
		WithIcon("⚠️").
		WithHeader("Harap periksa rincian pembelian Anda sebelum melanjutkan.").
		AddField("Paket", html.EscapeString(quote.PackageName)).
		AddField("Option Code", "<code>"+html.EscapeString(quote.Intent.OptionCode)+"</code>").
		AddField("Metode", "<code>"+strings.ToUpper(html.EscapeString(quote.Intent.Method))+"</code>").
		AddField("Total Bayar", priceLabel).
		AddField("Target Nomor", "<code>"+html.EscapeString(quote.Intent.MSISDN)+"</code>").
		WithRaw("<i>Harga canonical akan diverifikasi ulang saat Konfirmasi ditekan. Jika berubah, transaksi dibatalkan sebelum reserve/settlement.</i>").
		WithFooter("<i>Tekan Konfirmasi Pembayaran untuk menjalankan transaksi.</i>")

	screen := ui.NewScreen("myxl:checkout", "", card.Render())
	screen.AddRow(
		newMenuButton("✅ Konfirmasi Pembayaran", "myxl:checkout"),
		newMenuButton("❌ Batal", "myxl:cancel_draft"),
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
			if qrPayload, qrErr := normalizeQRPayload(result.QRCode); qrErr != nil {
				card.WithRaw("⚠️ <i>Payload QRIS dari operator tidak valid sehingga gambar QR tidak dapat dibuat.</i>")
			} else {
				wibLoc := time.FixedZone("WIB", 7*3600)
				expireWIB := time.Now().UTC().Add(pendingQRISTTL).In(wibLoc).Format("15:04:05")
				preview, truncated := inlineQRPreview(qrPayload)
				card.AddField("Batas Waktu", fmt.Sprintf("5 Menit (s/d %s WIB)", expireWIB))
				note := "<i>💡 Foto QRIS dikirimkan di bawah ini. QRIS berlaku 5 menit dan dapat dilihat kembali di Dashboard atau perintah <code>.myxl qris</code> selama belum dibayar.</i>"
				if truncated {
					note = "<i>💡 String dipersingkat agar aman untuk Telegram; payload penuh tetap tersedia pada foto QRIS.</i>"
				}
				card.WithRaw("📱 <b>Kode / String QRIS:</b>\n<code>" + html.EscapeString(preview) + "</code>\n\n" + note)
			}
		}
	}

	screen := ui.NewScreen("myxl:result", "", card.Render())
	optKey := m.RegisterOptionCode(optionCode)
	var firstRow []ui.Button
	firstRow = append(firstRow, newMenuButton("⭐ Simpan ke Favorit", fmt.Sprintf("myxl:bookmark_add:%s", optKey)))
	if result != nil && result.QRCode != "" {
		qrKey := m.RegisterQR(result.QRCode)
		if qrKey != "" {
			firstRow = append(firstRow, newMenuButton("🖼️ Kirim Foto QRIS", fmt.Sprintf("myxl:qris_img:%s", qrKey)))
		}
	}
	screen.AddRow(firstRow...)
	screen.AddRow(
		newMenuButton("📱 Buka Dashboard", "myxl:home"),
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
		screen := ui.NewScreen("myxl:pending_qris", "", card.Render())
		screen.AddRow(newMenuButton("🔙 Kembali ke Dashboard", "myxl:home"))
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

	qrPayload, qrErr := normalizeQRPayload(pending.QRCode)
	if qrErr != nil {
		card.WithRaw("⚠️ <i>Payload QRIS tersimpan tidak valid sehingga gambar QR tidak dapat dibuat.</i>")
	} else {
		preview, truncated := inlineQRPreview(qrPayload)
		note := "<i>💡 Foto QRIS dikirimkan ke chat. Anda dapat scan langsung atau upload dari galeri aplikasi e-wallet / mobile banking.</i>"
		if truncated {
			note = "<i>💡 String dipersingkat agar aman untuk Telegram; payload penuh tetap tersedia pada foto QRIS.</i>"
		}
		card.WithRaw("📱 <b>Kode / String QRIS:</b>\n<code>" + html.EscapeString(preview) + "</code>\n\n" + note)
	}

	screen := ui.NewScreen("myxl:pending_qris", "", card.Render())
	qrKey := ""
	if qrErr == nil {
		qrKey = m.RegisterQR(qrPayload)
	}
	var actionRow []ui.Button
	if qrKey != "" {
		actionRow = append(actionRow, newMenuButton("🖼️ Kirim Foto QRIS", fmt.Sprintf("myxl:qris_img:%s", qrKey)))
	}
	actionRow = append(actionRow, newMenuButton("🗑️ Batalkan", fmt.Sprintf("myxl:qris_cancel:%s", pending.TransactionCode)))
	screen.AddRow(actionRow...)
	screen.AddRow(
		newMenuButton("🔄 Cek Status", "myxl:pending_qris"),
		newMenuButton("🔙 Kembali ke Dashboard", "myxl:home"),
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
	screen := ui.NewScreen("myxl:del_pick", "", card.Render())
	for _, acc := range accounts {
		label := acc.MSISDN
		if acc.Alias != "" {
			label = fmt.Sprintf("%s (%s)", acc.MSISDN, acc.Alias)
		}
		screen.AddRow(
			newMenuButton("🗑️ "+truncateString(label, 20), fmt.Sprintf("myxl:del_ask:%s", acc.MSISDN)),
		)
	}
	screen.AddRow(newMenuButton("🔙 Batal", "myxl:accounts"))
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
	screen := ui.NewScreen("myxl:alias_pick", "", card.Render())
	for _, acc := range accounts {
		label := acc.MSISDN
		if acc.Alias != "" {
			label = fmt.Sprintf("%s (%s)", acc.MSISDN, acc.Alias)
		}
		screen.AddRow(
			newMenuButton("🏷️ "+truncateString(label, 20), fmt.Sprintf("myxl:alias_req:%s", acc.MSISDN)),
		)
	}
	screen.AddRow(newMenuButton("🔙 Batal", "myxl:accounts"))
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

	screen := ui.NewScreen("myxl:fam_list", "", card.Render())

	// 1. Number selection buttons row: [ 1 ] [ 2 ] [ 3 ] [ 4 ] [ 5 ]
	var numRow []ui.Button
	for i, item := range pageItems {
		globalNum := startIdx + i + 1
		label := fmt.Sprintf("%d", globalNum)
		optKey := m.RegisterOptionCode(item.Option.PackageOptionCode)
		numRow = append(numRow, newMenuButton(label, fmt.Sprintf("myxl:buy_opt:%s", optKey)))
	}
	if len(numRow) > 0 {
		screen.AddRow(numRow...)
	}

	// 2. Pagination row: [◀️ Prev] [📄 X/Y] [▶️ Next]
	var navRow []ui.Button
	if page > 1 {
		navRow = append(navRow, newMenuButton("◀️ Prev", fmt.Sprintf("myxl:fam_page:%s:%d", familyCode, page-1)))
	} else {
		navRow = append(navRow, newMenuButton("⏮️", "myxl:noop"))
	}
	navRow = append(navRow, newMenuButton(fmt.Sprintf("📄 %d/%d", page, totalPages), "myxl:noop"))
	if page < totalPages {
		navRow = append(navRow, newMenuButton("▶️ Next", fmt.Sprintf("myxl:fam_page:%s:%d", familyCode, page+1)))
	} else {
		navRow = append(navRow, newMenuButton("⏭️", "myxl:noop"))
	}
	screen.AddRow(navRow...)

	// 3. Back button
	screen.AddRow(newMenuButton("🔙 Kembali ke Store", "myxl:store"))

	return screen, nil
}
