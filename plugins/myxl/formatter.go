package myxl

import (
	"fmt"
	"html"
	"math"
	"strings"
	"time"
)

var fractionalBlocks = [8]string{"▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"}

// FormatBytes formats byte count as human readable string.
func FormatBytes(bytes float64) string {
	if bytes <= 0 || math.IsNaN(bytes) || math.IsInf(bytes, 0) {
		return "0 B"
	}
	const (
		kb = 1024.0
		mb = kb * 1024.0
		gb = mb * 1024.0
		tb = gb * 1024.0
	)

	if bytes >= tb {
		return fmt.Sprintf("%.2f TB", bytes/tb)
	} else if bytes >= gb {
		return fmt.Sprintf("%.2f GB", bytes/gb)
	} else if bytes >= mb {
		return fmt.Sprintf("%.2f MB", bytes/mb)
	} else if bytes >= kb {
		return fmt.Sprintf("%.2f KB", bytes/kb)
	}
	return fmt.Sprintf("%.0f B", bytes)
}

// RenderProgressBar renders a high-resolution progress bar with sub-block resolution.
func RenderProgressBar(remaining, total float64, width int) (string, string) {
	if width < 1 {
		width = 1
	} else if width > 64 {
		width = 64
	}
	if total <= 0 || remaining <= 0 || math.IsNaN(remaining) || math.IsNaN(total) || math.IsInf(remaining, 0) || math.IsInf(total, 0) {
		empty := strings.Repeat("░", width)
		return fmt.Sprintf("[%s]", empty), "0.0%"
	}

	clamped := math.Max(0, math.Min(remaining, total))
	percentage := (clamped / total) * 100.0

	totalFractions := int(math.Round((percentage / 100.0) * float64(width*8)))
	fullBlocks := totalFractions / 8
	remainder := totalFractions % 8

	var b strings.Builder
	b.Grow(width*4 + 2)
	b.WriteByte('[')

	for i := 0; i < fullBlocks && i < width; i++ {
		b.WriteString("█")
	}

	if remainder > 0 && fullBlocks < width {
		b.WriteString(fractionalBlocks[remainder-1])
		fullBlocks++
	}

	emptyBlocks := width - fullBlocks
	if emptyBlocks > 0 {
		b.WriteString(strings.Repeat("░", emptyBlocks))
	}

	b.WriteByte(']')
	return b.String(), fmt.Sprintf("%.1f%%", percentage)
}

// FormatWIBTime converts unix epoch (seconds or milliseconds) to readable WIB date.
func FormatWIBTime(epoch float64) string {
	if epoch <= 0 || math.IsNaN(epoch) || math.IsInf(epoch, 0) {
		return "N/A"
	}
	// If epoch is in milliseconds (greater than year 2100 in seconds: 4102444800)
	if epoch > 4102444800 {
		epoch /= 1000
	}
	wib := time.FixedZone("WIB", 7*3600)
	t := time.Unix(int64(epoch), 0).In(wib)
	return t.Format("02 Jan 2006 15:04 WIB")
}

func formatWIBDate(epoch float64, withYear bool) string {
	if epoch <= 0 || math.IsNaN(epoch) || math.IsInf(epoch, 0) {
		return "N/A"
	}
	if epoch > 4102444800 {
		epoch /= 1000
	}
	wib := time.FixedZone("WIB", 7*3600)
	t := time.Unix(int64(epoch), 0).In(wib)
	if withYear {
		return t.Format("02 Jan 2006")
	}
	return t.Format("02 Jan")
}

// FormatWIBClock formats a time.Time to "15:04:05 WIB".
func FormatWIBClock(t time.Time) string {
	wib := time.FixedZone("WIB", 7*3600)
	return t.In(wib).Format("15:04:05 WIB")
}

// FormatRemainingDuration formats a remaining duration into friendly "Xm Ys" format.
func FormatRemainingDuration(d time.Duration) string {
	if d <= 0 {
		return "Sudah Kedaluwarsa"
	}
	d = d.Round(time.Second)
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// MaskMSISDN masks middle digits of an MSISDN for privacy (e.g. 6281912345678 -> 6281****5678).
func MaskMSISDN(msisdn string) string {
	s := strings.TrimSpace(msisdn)
	if len(s) <= 7 {
		return s
	}
	prefix := s[:4]
	suffix := s[len(s)-4:]
	return prefix + "****" + suffix
}

// FormatQuotaResponse builds the Telegram HTML message for balance and quota.
func FormatQuotaResponse(account *Account, balance *BalanceData, quota *QuotaDetailsData, maskMSISDN bool) string {
	return FormatQuotaSnapshot(account, quotaSnapshot{
		Balance: balance,
		Quota:   quota,
	}, maskMSISDN)
}

const maxDashboardQuotaSummaryRunes = 2800

// FormatDashboardQuotaSummary renders a richer quota overview for the MyXL
// dashboard. It shows every benefit for as many active packages as fit in a
// conservative Telegram message budget, then points users to the full detail
// screen only when more packages remain.
func FormatDashboardQuotaSummary(quota *QuotaDetailsData) string {
	if quota == nil || len(quota.Quotas) == 0 {
		return "📦 <b>Paket Aktif</b>\n<i>Tidak ada paket kuota aktif.</i>"
	}

	var b strings.Builder
	b.WriteString("📦 <b>Paket Aktif</b>\n")
	shown := 0

	for i, q := range quota.Quotas {
		var section strings.Builder
		name := strings.TrimSpace(q.Name)
		if name == "" {
			name = "Paket Internet"
		}
		section.WriteString(fmt.Sprintf("\n%d. <b>%s</b>", i+1, html.EscapeString(name)))
		if q.ExpiredAt > 0 {
			section.WriteString(" · " + formatWIBDate(q.ExpiredAt, false))
		}
		section.WriteByte('\n')

		hasBenefits := false
		for _, benefit := range q.Benefits {
			bName := strings.TrimSpace(benefit.Name)
			if bName == "" {
				bName = benefit.DataType
			}
			if bName == "" {
				bName = "Kuota"
			}

			if strings.EqualFold(benefit.DataType, "DATA") || benefit.Total > 1000 {
				bar, _ := RenderProgressBar(benefit.Remaining, benefit.Total, 10)
				section.WriteString(fmt.Sprintf(
					"   %s  <code>%s</code> <i>%s / %s</i>\n",
					html.EscapeString(bName),
					bar,
					FormatBytes(benefit.Remaining),
					FormatBytes(benefit.Total),
				))
				hasBenefits = true
			} else if benefit.Total > 0 {
				section.WriteString(fmt.Sprintf(
					"   %s  <code>%.0f / %.0f %s</code>\n",
					html.EscapeString(bName),
					benefit.Remaining,
					benefit.Total,
					html.EscapeString(benefit.DataType),
				))
				hasBenefits = true
			}
		}
		if !hasBenefits {
			section.WriteString("   <i>Tidak ada rincian benefit.</i>\n")
		}

		candidate := b.String() + section.String()
		if shown > 0 && len([]rune(candidate)) > maxDashboardQuotaSummaryRunes {
			remaining := len(quota.Quotas) - shown
			b.WriteString(fmt.Sprintf(
				"\n… dan %d paket lainnya. Buka Rincian Kuota untuk daftar lengkap.",
				remaining,
			))
			break
		}

		b.WriteString(section.String())
		shown++
	}

	return strings.TrimSpace(b.String())
}
func FormatQuotaSnapshot(account *Account, snapshot quotaSnapshot, maskMSISDN bool) string {
	if account == nil {
		return "<i>Akun MyXL tidak tersedia.</i>"
	}

	var b strings.Builder
	displayNum := account.MSISDN
	if maskMSISDN {
		displayNum = MaskMSISDN(account.MSISDN)
	}

	b.WriteString("📱 <b>MyXL Account:</b> <code>")
	b.WriteString(html.EscapeString(displayNum))
	b.WriteString("</code>")
	if account.Alias != "" {
		b.WriteString(" (")
		b.WriteString(html.EscapeString(account.Alias))
		b.WriteString(")")
	}
	b.WriteString("\n")

	switch {
	case snapshot.BalanceErr != nil:
		b.WriteString("⚠️ <b>Pulsa:</b> <i>gagal dimuat</i>\n")
	case snapshot.Balance != nil:
		b.WriteString(fmt.Sprintf("💰 <b>Pulsa:</b> <code>Rp %s</code>\n", formatRupiah(int64(snapshot.Balance.Remaining))))
		if snapshot.Balance.ExpiredAt > 0 {
			b.WriteString(fmt.Sprintf("⏳ <b>Masa Aktif:</b> <code>%s</code>\n", FormatWIBTime(snapshot.Balance.ExpiredAt)))
		}
	default:
		b.WriteString("ℹ️ <b>Pulsa:</b> <i>data tidak tersedia</i>\n")
	}

	b.WriteString("\n")
	if snapshot.QuotaErr != nil {
		b.WriteString("⚠️ <b>Kuota:</b> <i>gagal dimuat</i>\n")
	} else if snapshot.Quota == nil || len(snapshot.Quota.Quotas) == 0 {
		b.WriteString("<i>Tidak ada paket kuota aktif ditemukan.</i>\n")
	} else {
		b.WriteString("📦 <b>Paket Aktif:</b>\n")
		for i, q := range snapshot.Quota.Quotas {
			name := strings.TrimSpace(q.Name)
			if name == "" {
				name = "Paket Internet"
			}

			b.WriteString(fmt.Sprintf("\n<b>%d. %s</b>\n", i+1, html.EscapeString(name)))
			if q.ExpiredAt > 0 {
				b.WriteString(fmt.Sprintf("   ⏳ <i>Berlaku s/d: %s</i>\n", FormatWIBTime(q.ExpiredAt)))
			}

			hasBenefits := false
			for _, benefit := range q.Benefits {
				bName := strings.TrimSpace(benefit.Name)
				if bName == "" {
					bName = benefit.DataType
				}
				if bName == "" {
					bName = "Quota"
				}

				if strings.EqualFold(benefit.DataType, "DATA") || benefit.Total > 1000 {
					bar, pct := RenderProgressBar(benefit.Remaining, benefit.Total, 10)
					b.WriteString(fmt.Sprintf("   ▫️ <b>%s:</b>\n", html.EscapeString(bName)))
					b.WriteString(fmt.Sprintf("      <code>%s %s</code>\n", bar, pct))
					b.WriteString(fmt.Sprintf("      <i>%s / %s</i>\n", FormatBytes(benefit.Remaining), FormatBytes(benefit.Total)))
					hasBenefits = true
				} else if benefit.Total > 0 {
					b.WriteString(fmt.Sprintf("   ▫️ <b>%s:</b> <code>%.0f / %.0f %s</code>\n",
						html.EscapeString(bName), benefit.Remaining, benefit.Total, benefit.DataType))
					hasBenefits = true
				}
			}
			if !hasBenefits {
				b.WriteString("   <i>Tidak ada rincian benefit.</i>\n")
			}
		}
	}

	if snapshot.BalanceErr != nil || snapshot.QuotaErr != nil {
		b.WriteString("\n🔄 <i>Sebagian data gagal dimuat. Gunakan Muat Ulang untuk mengambil ulang data yang gagal.</i>\n")
	}
	return b.String()
}
func formatRupiah(amount int64) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	s := fmt.Sprintf("%d", amount)
	n := len(s)
	if n <= 3 {
		return sign + s
	}

	var res strings.Builder
	res.WriteString(sign)
	rem := n % 3
	if rem > 0 {
		res.WriteString(s[:rem])
		if n > rem {
			res.WriteByte('.')
		}
	}
	for i := rem; i < n; i += 3 {
		res.WriteString(s[i : i+3])
		if i+3 < n {
			res.WriteByte('.')
		}
	}
	return res.String()
}

// FormatFamilyPackages formats the package family list with variants and options.
func FormatFamilyPackages(res *PackageListResponse) string {
	if res == nil || len(res.PackageVariants) == 0 {
		return "<i>Tidak ada paket ditemukan dalam family ini.</i>"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("<b>📦 Family: %s</b>\n", html.EscapeString(res.PackageFamily.Name)))
	if res.PackageFamily.PackageFamilyCode != "" {
		b.WriteString(fmt.Sprintf("<code>%s</code>\n\n", html.EscapeString(res.PackageFamily.PackageFamilyCode)))
	}

	idx := 1
	for _, v := range res.PackageVariants {
		b.WriteString(fmt.Sprintf("📁 <b>%s</b> (<code>%s</code>)\n", html.EscapeString(v.Name), html.EscapeString(v.PackageVariantCode)))
		for _, opt := range v.PackageOptions {
			priceStr := formatRupiah(int64(opt.Price))
			b.WriteString(fmt.Sprintf("  %d. <b>%s</b> — <code>Rp %s</code>\n", idx, html.EscapeString(opt.Name), priceStr))
			b.WriteString(fmt.Sprintf("     Kode: <code>%s</code>\n", html.EscapeString(opt.PackageOptionCode)))
			idx++
		}
		b.WriteString("\n")
	}
	b.WriteString("<i>Gunakan <code>.myxl paket &lt;kode&gt;</code> untuk melihat detail paket.</i>")
	return b.String()
}

// FormatPackageDetails renders details for a package option.
func FormatPackageDetails(details *PackageDetailsData) string {
	if details == nil {
		return "<i>Detail paket tidak tersedia.</i>"
	}
	var b strings.Builder
	b.WriteString("<b>📦 Detail Paket MyXL</b>\n\n")
	if details.PackageFamily.Name != "" {
		b.WriteString(fmt.Sprintf("<b>Family:</b> %s\n", html.EscapeString(details.PackageFamily.Name)))
	}
	if details.PackageOption != nil {
		b.WriteString(fmt.Sprintf("<b>Nama:</b> %s\n", html.EscapeString(details.PackageOption.Name)))
		b.WriteString(fmt.Sprintf("<b>Harga Resmi:</b> Rp %s\n", formatRupiah(int64(details.PackageOption.Price))))
		b.WriteString(fmt.Sprintf("<b>Option Code:</b> <code>%s</code>\n", html.EscapeString(details.PackageOption.PackageOptionCode)))
	}
	if details.TokenConfirmation != "" {
		b.WriteString("<b>Status Konfirmasi:</b> Tersedia\n")
	}
	if details.PackageOption != nil {
		b.WriteString("\n<i>Beli via: <code>.myxl buy " + html.EscapeString(details.PackageOption.PackageOptionCode) + " [pulsa/qris/gopay/ovo/dana/shopeepay] [harga]</code></i>")
	}
	return b.String()
}

// FormatSavedPackages renders the list of bookmarked packages.
func FormatSavedPackages(pkgs []*SavedPackage) string {
	if len(pkgs) == 0 {
		return "<i>Belum ada paket tersimpan (bookmark).\nGunakan <code>.myxl saved add &lt;option_code&gt;</code> untuk menyimpan paket.</i>"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("<b>📑 Daftar Paket Tersimpan (%d)</b>\n\n", len(pkgs)))
	for i, p := range pkgs {
		b.WriteString(fmt.Sprintf("%d. <b>%s</b> — <code>Rp %s</code>\n", i+1, html.EscapeString(p.Name), formatRupiah(p.Price)))
		b.WriteString(fmt.Sprintf("   Kode: <code>%s</code>", html.EscapeString(p.OptionCode)))
		if p.FamilyCode != "" {
			b.WriteString(fmt.Sprintf(" | Family: <code>%s</code>", html.EscapeString(p.FamilyCode)))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n<i>Beli langsung: <code>.myxl saved buy &lt;kode&gt; [metode] [harga]</code></i>")
	return b.String()
}

// FormatPurchaseResult renders the result of a purchase/settlement attempt.
func FormatPurchaseResult(res *SettlementResult, pkgName string, price int64, method string) string {
	if res == nil {
		return "<b>⚠️ Hasil transaksi tidak tersedia.</b>"
	}

	var b strings.Builder
	pendingQRIS := res.IsSuccess && strings.Contains(strings.ToLower(method), "qris")
	switch {
	case pendingQRIS:
		b.WriteString("<b>⏳ Menunggu Pembayaran QRIS</b>\n\n")
	case res.IsSuccess:
		b.WriteString("<b>✅ Pembelian Berhasil</b>\n\n")
	default:
		b.WriteString(fmt.Sprintf("<b>❌ Pembelian Gagal: %s</b>\n\n", html.EscapeString(res.Status)))
	}

	if pkgName != "" {
		b.WriteString(fmt.Sprintf("<b>Paket:</b> %s\n", html.EscapeString(pkgName)))
	}
	b.WriteString(fmt.Sprintf("<b>Nominal:</b> Rp %s\n", formatRupiah(price)))
	b.WriteString(fmt.Sprintf("<b>Metode Pembayaran:</b> <code>%s</code>\n", html.EscapeString(method)))
	if pendingQRIS {
		b.WriteString("<b>Status:</b> Menunggu pembayaran\n")
	}
	if res.TransactionCode != "" {
		b.WriteString(fmt.Sprintf("<b>ID Transaksi:</b> <code>%s</code>\n", html.EscapeString(res.TransactionCode)))
	}
	if res.Message != "" {
		b.WriteString(fmt.Sprintf("<b>Pesan Operator:</b> %s\n", html.EscapeString(res.Message)))
	}
	if res.Deeplink != "" {
		b.WriteString(fmt.Sprintf("\n🔗 <a href=\"%s\">Klik di sini untuk melanjutkan pembayaran</a>\n", html.EscapeString(res.Deeplink)))
	}
	if res.QRCode != "" {
		if qrPayload, qrErr := normalizeQRPayload(res.QRCode); qrErr != nil {
			b.WriteString("\n⚠️ <i>Payload QRIS dari operator tidak valid sehingga gambar QR tidak dapat dibuat.</i>\n")
		} else {
			preview, truncated := inlineQRPreview(qrPayload)
			b.WriteString("\n<b>📱 QRIS String:</b>\n")
			b.WriteString(fmt.Sprintf("<code>%s</code>\n", html.EscapeString(preview)))
			if truncated {
				b.WriteString("<i>String dipersingkat di pesan ini; payload penuh tetap tersedia pada foto QRIS.</i>\n")
			} else {
				b.WriteString("<i>Salin kode QRIS di atas atau scan foto QRIS melalui aplikasi e-wallet / mobile banking.</i>\n")
			}
		}
	}
	if pendingQRIS {
		b.WriteString("\n<i>Tagihan ini tersimpan sementara di bot. Menghapusnya dari bot tidak membatalkan pembayaran atau tagihan di operator.</i>\n")
	}
	return b.String()
}
