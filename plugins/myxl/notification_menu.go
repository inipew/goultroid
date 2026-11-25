package myxl

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/inipew/goultroid/internal/ui"
)

func (m *MenuManager) BuildMaintenanceNotifyScreen(ctx context.Context) (*ui.Screen, error) {
	if m == nil || m.plugin == nil {
		return nil, fmt.Errorf("myxl plugin unavailable")
	}
	status, err := m.plugin.maintenanceService().GetStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("load maintenance notify status: %w", err)
	}
	cfg := status.Config
	screen := ui.NewScreen("myxl:maint_notify", "", formatNotifyStatusHTML(&cfg))

	evLabel := func(name string, on bool) string {
		if on {
			return "✅ " + name + ": ON"
		}
		return "❌ " + name + ": OFF"
	}
	screen.AddRow(
		newMenuButton(evLabel("Completion", cfg.NotifyOnCompletion), "myxl:maint_notify_ev:completion"),
		newMenuButton(evLabel("Partial Failure", cfg.NotifyOnPartialFailure), "myxl:maint_notify_ev:partial_failure"),
	)
	screen.AddRow(
		newMenuButton(evLabel("Failure", cfg.NotifyOnFailure), "myxl:maint_notify_ev:failure"),
		newMenuButton(evLabel("Cancelled", cfg.NotifyOnCancelled), "myxl:maint_notify_ev:cancelled"),
	)

	for _, d := range cfg.Destinations {
		icon := "⚪"
		if d.Enabled {
			icon = "🟢"
		}
		label := fmt.Sprintf("%s %s (%s)", icon, d.Label, d.Type)
		screen.AddRow(newMenuButton(label, "myxl:maint_notify_dest:"+d.ID))
	}

	if len(cfg.Destinations) < MaxNotificationDestinations {
		screen.AddRow(newMenuButton("➕ Tambah Destination", "myxl:maint_notify_add"))
	}

	screen.AddRow(
		newMenuButton("🔄 Muat Ulang", "myxl:maint_notify"),
		newMenuButton("🔙 Kembali", "myxl:maintenance"),
		newMenuButton("🏠 Home", "myxl:home"),
	)
	return screen, nil
}

func (m *MenuManager) BuildMaintenanceNotifyDestScreen(ctx context.Context, destID string) (*ui.Screen, error) {
	if m == nil || m.plugin == nil {
		return nil, fmt.Errorf("myxl plugin unavailable")
	}
	status, err := m.plugin.maintenanceService().GetStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("load maintenance notify status: %w", err)
	}
	dest, ok := findNotificationDestination(&status.Config, destID)
	if !ok {
		return nil, fmt.Errorf("notification destination %q not found", destID)
	}

	stateStr := "OFF"
	toggleLabel := "▶️ Aktifkan"
	if dest.Enabled {
		stateStr = "ON"
		toggleLabel = "⏸️ Nonaktifkan"
	}

	var b strings.Builder
	b.WriteString("📍 <b>Detail Destination Notifikasi</b>\n\n")
	fmt.Fprintf(&b, "• ID: <code>%s</code>\n", html.EscapeString(dest.ID))
	fmt.Fprintf(&b, "• Label: <b>%s</b>\n", html.EscapeString(dest.Label))
	fmt.Fprintf(&b, "• Tipe: <code>%s</code>\n", html.EscapeString(string(dest.Type)))
	fmt.Fprintf(&b, "• Status: <b>%s</b>\n", stateStr)
	if dest.PeerID != 0 {
		fmt.Fprintf(&b, "• Peer ID: <code>%s</code>\n", html.EscapeString(maskPeerID(dest.PeerID)))
	}

	screen := ui.NewScreen("myxl:maint_notify_dest", "", b.String())
	screen.AddRow(
		newMenuButton(toggleLabel, "myxl:maint_notify_dest_toggle:"+dest.ID),
		newMenuButton("🧪 Tes Kirim", "myxl:maint_notify_dest_test:"+dest.ID),
	)
	screen.AddRow(
		newMenuButton("🗑️ Hapus Destination", "myxl:maint_notify_dest_del_ask:"+dest.ID),
	)
	screen.AddRow(
		newMenuButton("🔙 Kembali", "myxl:maint_notify"),
		newMenuButton("🏠 Home", "myxl:home"),
	)
	return screen, nil
}

func (m *MenuManager) BuildMaintenanceNotifyDestDeleteConfirmScreen(ctx context.Context, destID string) (*ui.Screen, error) {
	if m == nil || m.plugin == nil {
		return nil, fmt.Errorf("myxl plugin unavailable")
	}
	status, err := m.plugin.maintenanceService().GetStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("load maintenance notify status: %w", err)
	}
	dest, ok := findNotificationDestination(&status.Config, destID)
	if !ok {
		return nil, fmt.Errorf("notification destination %q not found", destID)
	}

	text := fmt.Sprintf(
		"⚠️ <b>Hapus Destination Notifikasi?</b>\n\n"+
			"• ID: <code>%s</code>\n"+
			"• Label: <b>%s</b>\n"+
			"• Tipe: <code>%s</code>\n\n"+
			"Jika tidak ada destination tersisa, notifikasi akan kembali menggunakan fallback implicit ke Owner PM.",
		html.EscapeString(dest.ID),
		html.EscapeString(dest.Label),
		html.EscapeString(string(dest.Type)),
	)
	screen := ui.NewScreen("myxl:maint_notify_dest_del_ask", "", text)
	screen.AddRow(
		newMenuButton("✅ Ya, Hapus", "myxl:maint_notify_dest_del_confirm:"+dest.ID),
		newMenuButton("❌ Batal", "myxl:maint_notify_dest:"+dest.ID),
	)
	return screen, nil
}

func (m *MenuManager) BuildMaintenanceNotifyAddScreen(ctx context.Context) (*ui.Screen, error) {
	if m == nil || m.plugin == nil {
		return nil, fmt.Errorf("myxl plugin unavailable")
	}
	text := "➕ <b>Tambah Destination Notifikasi</b>\n\n" +
		"Pilih tipe tujuan notifikasi:\n" +
		"• <b>Owner PM</b> — langsung ke akun owner\n" +
		"• <b>Global Log</b> — menggunakan userlog global aktif\n" +
		"• <b>Group</b> — basic group via ID atau command di chat target\n" +
		"• <b>Supergroup</b> — <code>@username</code> publik, atau ID <code>-100...</code> jika metadata peer &amp; access hash sudah tersimpan\n" +
		"• <b>Bot PM</b> — bot via <code>@username</code> atau ID (jika sudah tersimpan)\n" +
		"• <b>Channel</b> — <code>@username</code> publik, atau ID <code>-100...</code> jika metadata peer &amp; access hash sudah tersimpan\n\n" +
		"<i>Untuk supergroup/channel privat tanpa username, cara paling andal adalah menjalankan <code>.myxl schedule notify destination add &lt;type&gt;</code> langsung di chat tujuan. Destination baru akan diuji kirim sebelum disimpan.</i>"
	screen := ui.NewScreen("myxl:maint_notify_add", "", text)
	screen.AddRow(
		newMenuButton("👤 Owner PM", "myxl:maint_notify_add_type:owner"),
		newMenuButton("📋 Global Log", "myxl:maint_notify_add_type:global_log"),
	)
	screen.AddRow(
		newMenuButton("👥 Group", "myxl:maint_notify_add_type:group"),
		newMenuButton("🏢 Supergroup", "myxl:maint_notify_add_type:supergroup"),
	)
	screen.AddRow(
		newMenuButton("🤖 Bot PM", "myxl:maint_notify_add_type:bot_pm"),
		newMenuButton("📢 Channel", "myxl:maint_notify_add_type:channel"),
	)
	screen.AddRow(
		newMenuButton("🔙 Kembali", "myxl:maint_notify"),
		newMenuButton("🏠 Home", "myxl:home"),
	)
	return screen, nil
}

func findNotificationDestination(cfg *TokenMaintenanceConfig, id string) (NotificationDestination, bool) {
	if cfg == nil {
		return NotificationDestination{}, false
	}
	trimmed := strings.TrimSpace(id)
	for _, d := range cfg.Destinations {
		if d.ID == trimmed {
			return d, true
		}
	}
	return NotificationDestination{}, false
}
