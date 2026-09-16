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
	if total <= 0 || remaining <= 0 || math.IsNaN(remaining) || math.IsNaN(total) {
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
	if epoch <= 0 {
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

	if balance != nil {
		b.WriteString(fmt.Sprintf("💰 <b>Pulsa:</b> <code>Rp %s</code>\n", formatRupiah(int64(balance.Remaining))))
		if balance.ExpiredAt > 0 {
			b.WriteString(fmt.Sprintf("⏳ <b>Masa Aktif:</b> <code>%s</code>\n", FormatWIBTime(balance.ExpiredAt)))
		}
	}

	b.WriteString("\n")

	if quota == nil || len(quota.Quotas) == 0 {
		b.WriteString("<i>Tidak ada paket kuota aktif ditemukan.</i>\n")
		return b.String()
	}

	b.WriteString("📦 <b>Paket Aktif:</b>\n")

	for i, q := range quota.Quotas {
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

			// If data quota (measured in bytes)
			if strings.EqualFold(benefit.DataType, "DATA") || benefit.Total > 1000 {
				bar, pct := RenderProgressBar(benefit.Remaining, benefit.Total, 10)
				b.WriteString(fmt.Sprintf("   ▫️ <b>%s:</b>\n", html.EscapeString(bName)))
				b.WriteString(fmt.Sprintf("      <code>%s %s</code>\n", bar, pct))
				b.WriteString(fmt.Sprintf("      <i>%s / %s</i>\n", FormatBytes(benefit.Remaining), FormatBytes(benefit.Total)))
				hasBenefits = true
			} else if benefit.Total > 0 {
				// Voice or SMS
				b.WriteString(fmt.Sprintf("   ▫️ <b>%s:</b> <code>%.0f / %.0f %s</code>\n",
					html.EscapeString(bName), benefit.Remaining, benefit.Total, benefit.DataType))
				hasBenefits = true
			}
		}

		if !hasBenefits {
			b.WriteString("   <i>Tidak ada rincian benefit.</i>\n")
		}
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
