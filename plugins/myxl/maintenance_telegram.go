package myxl

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func (p *Plugin) maintenanceService() *MaintenanceService {
	if p == nil {
		return nil
	}
	if p.maintSvc == nil {
		p.maintSvc = NewMaintenanceService(p)
	}
	return p.maintSvc
}

func (p *Plugin) handleScheduleCommand(ctx *core.Context, args []string) error {
	if ctx != nil && ctx.Perms != nil && !ctx.IsOwner() {
		return ctx.Status("🔒 Hanya owner yang dapat mengelola jadwal maintenance token MyXL.")
	}
	svc := p.maintenanceService()
	cCtx, cancel := context.WithTimeout(getContext(ctx), 15*time.Second)
	defer cancel()

	if len(args) == 0 {
		view, err := svc.GetStatus(cCtx)
		if err != nil {
			return ctx.Fail(err, "Gagal membaca status jadwal maintenance.")
		}
		return deliverHTML(ctx, formatMaintenanceStatusHTML(view))
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]

	switch sub {
	case "status":
		view, err := svc.GetStatus(cCtx)
		if err != nil {
			return ctx.Fail(err, "Gagal membaca status jadwal maintenance.")
		}
		return deliverHTML(ctx, formatMaintenanceStatusHTML(view))

	case "enable", "on":
		cfg, err := svc.SetEnabled(cCtx, true)
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("mengaktifkan jadwal", err))
		}
		return ctx.Success(fmt.Sprintf("✅ Jadwal maintenance token MyXL <b>diaktifkan</b> (interval <code>%s</code>, anchor <code>%s</code> <code>%s</code>).", cfg.Interval, html.EscapeString(cfg.AnchorTime), html.EscapeString(cfg.Timezone)))

	case "disable", "off":
		_, err := svc.SetEnabled(cCtx, false)
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("menonaktifkan jadwal", err))
		}
		return ctx.Success("⏸️ Jadwal maintenance token MyXL <b>dinonaktifkan</b>. Pengaturan sebelumnya tetap tersimpan.")

	case "interval":
		if len(rest) == 0 {
			return ctx.Status("Format: <code>.myxl schedule interval &lt;durasi&gt;</code>\nContoh: <code>.myxl schedule interval 6h</code> (1h–24h)")
		}
		dur, err := time.ParseDuration(strings.TrimSpace(rest[0]))
		if err != nil {
			return ctx.Status("Format interval tidak valid. Contoh: <code>1h</code>, <code>3h</code>, <code>6h</code>, <code>12h</code>, <code>24h</code>.")
		}
		cfg, err := svc.SetInterval(cCtx, dur)
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("mengatur interval", err))
		}
		return ctx.Success(fmt.Sprintf("✅ Interval maintenance diatur ke <code>%s</code>.", cfg.Interval))

	case "time", "anchor":
		if len(rest) == 0 {
			return ctx.Status("Format: <code>.myxl schedule time &lt;HH:MM&gt;</code>\nContoh: <code>.myxl schedule time 03:00</code>")
		}
		cfg, err := svc.SetAnchorTime(cCtx, rest[0])
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("mengatur waktu anchor", err))
		}
		return ctx.Success(fmt.Sprintf("✅ Waktu anchor maintenance diatur ke <code>%s</code> (<code>%s</code>).", html.EscapeString(cfg.AnchorTime), html.EscapeString(cfg.Timezone)))

	case "timezone", "tz":
		if len(rest) == 0 {
			return ctx.Status("Format: <code>.myxl schedule timezone &lt;IANA_TZ&gt;</code>\nContoh: <code>.myxl schedule timezone Asia/Jakarta</code>")
		}
		cfg, err := svc.SetTimezone(cCtx, rest[0])
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("mengatur timezone", err))
		}
		return ctx.Success(fmt.Sprintf("✅ Timezone maintenance diatur ke <code>%s</code>.", html.EscapeString(cfg.Timezone)))

	case "window":
		if len(rest) == 0 {
			return ctx.Status("Format: <code>.myxl schedule window &lt;durasi&gt;</code>\nContoh: <code>.myxl schedule window 30m</code> (5m–120m)")
		}
		dur, err := time.ParseDuration(strings.TrimSpace(rest[0]))
		if err != nil {
			return ctx.Status("Format jendela refresh tidak valid. Contoh: <code>15m</code>, <code>30m</code>, <code>60m</code>.")
		}
		cfg, err := svc.SetRefreshWindow(cCtx, dur)
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("mengatur jendela refresh", err))
		}
		return ctx.Success(fmt.Sprintf("✅ Jendela refresh diatur ke <code>%s</code> sebelum expiry.", cfg.RefreshWindow))

	case "accounts", "scope":
		if len(rest) == 0 {
			return ctx.Status("Format: <code>.myxl schedule accounts &lt;all|active|selected nomor1,nomor2&gt;</code>")
		}
		scope := AccountScope(strings.ToLower(strings.TrimSpace(rest[0])))
		var selected []string
		if scope == AccountScopeSelected && len(rest) > 1 {
			for _, part := range strings.Split(strings.Join(rest[1:], ","), ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					selected = append(selected, trimmed)
				}
			}
		}
		cfg, err := svc.SetAccountScope(cCtx, scope, selected)
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("mengatur scope akun", err))
		}
		return ctx.Success(fmt.Sprintf("✅ Scope akun maintenance diatur ke <b>%s</b>.", html.EscapeString(formatAccountScopeLabel(cfg))))

	case "run":
		if err := svc.TriggerRun(cCtx); err != nil {
			if errors.Is(err, ErrMaintenanceAlreadyRunning) {
				return ctx.Status("⏳ Maintenance token sedang berjalan. Tunggu hingga siklus selesai.")
			}
			return ctx.Status(formatMaintenanceMutationError("menjalankan maintenance", err))
		}
		return ctx.Success("▶️ Permintaan maintenance token telah dikirim ke JobManager / TaskEngine.")

	case "notify":
		return p.handleScheduleNotifyCommand(ctx, cCtx, rest)

	case "history", "riwayat":
		runs, err := svc.GetHistory(cCtx, 10)
		if err != nil {
			return ctx.Fail(err, "Gagal membaca riwayat maintenance.")
		}
		return deliverHTML(ctx, formatMaintenanceHistoryHTML(runs))

	default:
		return ctx.Status(
			"Format subcommand <code>.myxl schedule</code> tidak dikenal.\n\n" +
				"Gunakan:\n" +
				"• <code>.myxl schedule status</code>\n" +
				"• <code>.myxl schedule enable</code> | <code>disable</code>\n" +
				"• <code>.myxl schedule interval &lt;1h..24h&gt;</code>\n" +
				"• <code>.myxl schedule time &lt;HH:MM&gt;</code>\n" +
				"• <code>.myxl schedule timezone &lt;IANA_TZ&gt;</code>\n" +
				"• <code>.myxl schedule window &lt;5m..120m&gt;</code>\n" +
				"• <code>.myxl schedule accounts &lt;all|active|selected ...&gt;</code>\n" +
				"• <code>.myxl schedule run</code>\n" +
				"• <code>.myxl schedule history</code>",
		)
	}
}

func formatMaintenanceMutationError(action string, err error) string {
	if errors.Is(err, ErrScheduleSyncRollbackFailed) {
		return fmt.Sprintf("⚠️ Gagal %s dan rollback konfigurasi juga gagal (%s). Jalankan <code>.myxl schedule disable</code> atau simpan ulang konfigurasi untuk menyinkronkan jadwal.", html.EscapeString(action), html.EscapeString(err.Error()))
	}
	return fmt.Sprintf("⚠️ Gagal %s: %s", html.EscapeString(action), html.EscapeString(err.Error()))
}

func formatAccountScopeLabel(cfg *TokenMaintenanceConfig) string {
	if cfg == nil {
		return string(AccountScopeAll)
	}
	if cfg.AccountScope == AccountScopeSelected {
		return fmt.Sprintf("selected (%d akun)", len(cfg.SelectedMSISDNs))
	}
	return string(cfg.AccountScope)
}

func formatMaintenanceStatusHTML(view *MaintenanceStatusView) string {
	if view == nil {
		return "ℹ️ Status maintenance tidak tersedia."
	}
	cfg := view.Config
	stateLabel := "⏸️ Nonaktif"
	if cfg.Enabled {
		stateLabel = "🟢 Aktif"
	}
	var b strings.Builder
	b.WriteString("🕒 <b>MyXL Token Maintenance</b>\n\n")
	fmt.Fprintf(&b, "• <b>Status Jadwal:</b> %s\n", stateLabel)
	if view.Running {
		b.WriteString("• <b>Eksekusi Saat Ini:</b> ⏳ Sedang berjalan\n")
	}
	fmt.Fprintf(&b, "• <b>Interval:</b> <code>%s</code>\n", cfg.Interval)
	fmt.Fprintf(&b, "• <b>Anchor Time:</b> <code>%s</code>\n", html.EscapeString(cfg.AnchorTime))
	fmt.Fprintf(&b, "• <b>Timezone:</b> <code>%s</code>\n", html.EscapeString(cfg.Timezone))
	fmt.Fprintf(&b, "• <b>Refresh Window:</b> <code>%s</code>\n", cfg.RefreshWindow)
	fmt.Fprintf(&b, "• <b>Scope Akun:</b> <code>%s</code>\n", html.EscapeString(formatAccountScopeLabel(&cfg)))
	fmt.Fprintf(&b, "• <b>Notifikasi:</b> selesai=%t, gagal=%t\n", cfg.NotifyOnCompletion, cfg.NotifyOnFailure)

	b.WriteString("\n📊 <b>Eksekusi Terakhir:</b>\n")
	if view.LastRun == nil {
		b.WriteString("<i>Belum ada riwayat eksekusi.</i>")
	} else {
		r := view.LastRun
		fmt.Fprintf(&b, "• <b>Hasil:</b> <code>%s</code> (%s)\n", html.EscapeString(string(r.Status)), html.EscapeString(r.TriggerSource))
		fmt.Fprintf(&b, "• <b>Waktu:</b> <code>%s</code> (%s)\n", r.StartedAt.UTC().Format("2006-01-02 15:04 UTC"), r.Duration.Round(time.Millisecond))
		fmt.Fprintf(&b, "• <b>Ringkasan:</b> total=%d, refreshed=%d, fresh=%d, skipped=%d, failed=%d, cancelled=%d",
			r.TotalAccounts, r.RefreshedCount, r.AlreadyFreshCount, r.SkippedCount, r.FailedCount, r.CancelledCount)
	}
	return b.String()
}

func formatMaintenanceHistoryHTML(runs []*MaintenanceRunSummary) string {
	var b strings.Builder
	b.WriteString("📜 <b>Riwayat Token Maintenance MyXL</b>\n\n")
	if len(runs) == 0 {
		b.WriteString("<i>Belum ada riwayat eksekusi.</i>")
		return b.String()
	}
	for i, r := range runs {
		fmt.Fprintf(&b, "%d. <code>%s</code> · <b>%s</b> (%s)\n",
			i+1,
			r.StartedAt.UTC().Format("02 Jan 15:04 UTC"),
			html.EscapeString(string(r.Status)),
			html.EscapeString(r.TriggerSource),
		)
		fmt.Fprintf(&b, "   total=%d · refreshed=%d · fresh=%d · skipped=%d · failed=%d · cancelled=%d (%s)\n",
			r.TotalAccounts, r.RefreshedCount, r.AlreadyFreshCount, r.SkippedCount, r.FailedCount, r.CancelledCount, r.Duration.Round(time.Millisecond))
	}
	return b.String()
}
