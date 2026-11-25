package myxl

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func parseNotifyToggleValue(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "enable", "1":
		return true, nil
	case "off", "false", "disable", "0":
		return false, nil
	default:
		return false, errors.New("nilai notifikasi harus on atau off")
	}
}

func parseNotifyEventKey(raw string) (NotificationEvent, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "completion", "complete", "selesai":
		return EventCompletion, nil
	case "partial", "partial_failure", "sebagian":
		return EventPartialFailure, nil
	case "failure", "fail", "gagal":
		return EventFailure, nil
	case "cancelled", "cancel", "batal":
		return EventCancelled, nil
	default:
		return EventNone, errors.New("event harus completion, partial_failure, failure, atau cancelled")
	}
}

func (p *Plugin) handleScheduleNotifyCommand(ctx *core.Context, cCtx context.Context, args []string) error {
	svc := p.maintenanceService()
	if len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "status")) {
		view, err := svc.GetStatus(cCtx)
		if err != nil {
			return ctx.Fail(err, "Gagal membaca status notifikasi.")
		}
		return deliverHTML(ctx, formatNotifyStatusHTML(&view.Config))
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]

	if sub == "destination" || sub == "dest" {
		return p.handleNotifyDestinationCommand(ctx, cCtx, svc, rest)
	}

	event, err := parseNotifyEventKey(sub)
	if err != nil {
		return ctx.Status(notifyCommandUsageHTML())
	}
	if len(rest) != 1 {
		return ctx.Status(fmt.Sprintf("Format: <code>.myxl schedule notify %s &lt;on|off&gt;</code>", html.EscapeString(string(event))))
	}
	enabled, err := parseNotifyToggleValue(rest[0])
	if err != nil {
		return ctx.Status("Nilai notifikasi harus <code>on</code> atau <code>off</code>.")
	}
	cfg, err := svc.SetNotifyEvent(cCtx, event, enabled)
	if err != nil {
		return ctx.Status(formatMaintenanceMutationError("mengatur notifikasi", err))
	}
	stateStr := "OFF"
	if enabled {
		stateStr = "ON"
	}
	return ctx.Success(fmt.Sprintf("✅ Notifikasi <b>%s</b> diatur ke <code>%s</code>.\n\n%s",
		html.EscapeString(string(event)), stateStr, formatNotifyEventsSummaryLine(cfg)))
}

func (p *Plugin) handleNotifyDestinationCommand(ctx *core.Context, cCtx context.Context, svc *MaintenanceService, args []string) error {
	if len(args) == 0 {
		return ctx.Status(notifyDestinationUsageHTML())
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]

	switch action {
	case "list":
		if len(rest) != 0 {
			return ctx.Status("Format: <code>.myxl schedule notify destination list</code>")
		}
		view, err := svc.GetStatus(cCtx)
		if err != nil {
			return ctx.Fail(err, "Gagal membaca daftar destination.")
		}
		return deliverHTML(ctx, formatNotifyDestinationsHTML(&view.Config))

	case "add":
		if len(rest) < 1 || len(rest) > 2 {
			return ctx.Status("Format: <code>.myxl schedule notify destination add &lt;owner|global_log|group|supergroup|bot_pm|channel&gt; [target]</code>")
		}
		destType := NotificationDestinationType(strings.ToLower(strings.TrimSpace(rest[0])))
		target := ""
		if len(rest) == 2 {
			target = strings.TrimSpace(rest[1])
		}
		dest, err := p.buildDestinationFromCommand(cCtx, ctx, destType, target)
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("menambahkan destination", err))
		}
		cfg, err := svc.AddNotificationDestination(cCtx, dest)
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("menambahkan destination", err))
		}
		return ctx.Success(fmt.Sprintf("✅ Destination <code>%s</code> (<b>%s</b>) berhasil ditambahkan.\n\n%s",
			html.EscapeString(dest.ID), html.EscapeString(string(dest.Type)), formatNotifyDestinationsHTML(cfg)))

	case "remove", "rm", "del":
		if len(rest) != 1 {
			return ctx.Status("Format: <code>.myxl schedule notify destination remove &lt;id&gt;</code>")
		}
		cfg, err := svc.RemoveNotificationDestination(cCtx, rest[0])
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("menghapus destination", err))
		}
		return ctx.Success(fmt.Sprintf("🗑️ Destination <code>%s</code> dihapus.\n\n%s",
			html.EscapeString(strings.TrimSpace(rest[0])), formatNotifyDestinationsHTML(cfg)))

	case "enable":
		if len(rest) != 1 {
			return ctx.Status("Format: <code>.myxl schedule notify destination enable &lt;id&gt;</code>")
		}
		cfg, err := svc.SetNotificationDestinationEnabled(cCtx, rest[0], true)
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("mengaktifkan destination", err))
		}
		return ctx.Success(fmt.Sprintf("✅ Destination <code>%s</code> diaktifkan.\n\n%s",
			html.EscapeString(strings.TrimSpace(rest[0])), formatNotifyDestinationsHTML(cfg)))

	case "disable":
		if len(rest) != 1 {
			return ctx.Status("Format: <code>.myxl schedule notify destination disable &lt;id&gt;</code>")
		}
		cfg, err := svc.SetNotificationDestinationEnabled(cCtx, rest[0], false)
		if err != nil {
			return ctx.Status(formatMaintenanceMutationError("menonaktifkan destination", err))
		}
		return ctx.Success(fmt.Sprintf("⏸️ Destination <code>%s</code> dinonaktifkan.\n\n%s",
			html.EscapeString(strings.TrimSpace(rest[0])), formatNotifyDestinationsHTML(cfg)))

	case "test":
		if len(rest) != 1 {
			return ctx.Status("Format: <code>.myxl schedule notify destination test &lt;id&gt;</code>")
		}
		if err := svc.TestNotificationDestination(cCtx, rest[0]); err != nil {
			return ctx.Status(formatMaintenanceMutationError("mengirim test notifikasi", err))
		}
		return ctx.Success(fmt.Sprintf("🧪 Test notifikasi berhasil dikirim ke <code>%s</code>.", html.EscapeString(strings.TrimSpace(rest[0]))))

	default:
		return ctx.Status(notifyDestinationUsageHTML())
	}
}

var (
	errNotifyDestTypeMismatch = errors.New("destination type mismatch")
)

func (p *Plugin) buildDestinationFromCommand(
	cCtx context.Context,
	cmdCtx *core.Context,
	destType NotificationDestinationType,
	target string,
) (NotificationDestination, error) {
	switch destType {
	case DestOwner:
		if target != "" {
			return NotificationDestination{}, errors.New("destination owner tidak memerlukan argumen target")
		}
		p.maintMu.RLock()
		ownerID := p.ownerID
		p.maintMu.RUnlock()
		if ownerID <= 0 {
			return NotificationDestination{}, errors.New("runtime owner id tidak tersedia")
		}
		return NotificationDestination{ID: "owner", Type: DestOwner, Label: "Owner PM", Enabled: true}, nil

	case DestGlobalLog:
		if target != "" {
			return NotificationDestination{}, errors.New("destination global_log tidak memerlukan argumen target")
		}
		p.maintMu.RLock()
		ulog := p.userlogSvc
		p.maintMu.RUnlock()
		if ulog == nil {
			return NotificationDestination{}, errors.New("layanan userlog global tidak tersedia")
		}
		logDest, err := ulog.GetDestination(cCtx)
		if err != nil || logDest == nil || logDest.ID <= 0 {
			return NotificationDestination{}, errors.New("global log destination belum dikonfigurasi")
		}
		return NotificationDestination{ID: "global_log", Type: DestGlobalLog, Label: "Global Log", Enabled: true}, nil

	case DestGroup, DestSupergroup, DestChannel, DestBotPM:
		var resolved tg.InputPeerClass
		var label string

		if target == "" {
			if destType == DestBotPM {
				return NotificationDestination{}, errors.New("destination bot_pm memerlukan target @username atau ID bot")
			}
			if cmdCtx == nil || cmdCtx.PeerID == nil {
				return NotificationDestination{}, errors.New("jalankan perintah ini di chat tujuan atau sertakan target @username")
			}
			resolved = cmdCtx.PeerID
			if cmdCtx.Chat != nil {
				label = cmdCtx.Chat.Title
			}
		} else {
			p.maintMu.RLock()
			resolver := p.peerResolver
			p.maintMu.RUnlock()
			if resolver == nil {
				return NotificationDestination{}, errors.New("peer resolver tidak tersedia; jalankan perintah di chat tujuan")
			}
			peer, err := resolver.Resolve(cCtx, target)
			if err != nil {
				switch {
				case errors.Is(err, core.ErrAccessHashMissing):
					return NotificationDestination{}, fmt.Errorf("%w: access hash peer belum tersedia di cache/storage; jalankan .myxl schedule notify destination add %s langsung di chat tujuan", core.ErrAccessHashMissing, destType)
				case errors.Is(err, core.ErrNotFound):
					return NotificationDestination{}, fmt.Errorf("%w: peer tidak ditemukan, periksa @username atau ID tujuan", core.ErrNotFound)
				case errors.Is(err, core.ErrInvalidArgs):
					return NotificationDestination{}, fmt.Errorf("%w: format target peer tidak valid", core.ErrInvalidArgs)
				default:
					return NotificationDestination{}, errors.New("peer tidak dapat di-resolve saat ini")
				}
			}
			resolved = peer
			label = target
		}

		var dest NotificationDestination
		switch destType {
		case DestGroup:
			chatPeer, ok := resolved.(*tg.InputPeerChat)
			if !ok {
				return NotificationDestination{}, fmt.Errorf("%w: target bukan basic group (gunakan tipe supergroup atau channel)", errNotifyDestTypeMismatch)
			}
			dest = NotificationDestination{
				ID:      fmt.Sprintf("grp-%d", chatPeer.ChatID),
				Type:    DestGroup,
				Label:   label,
				Enabled: true,
				PeerID:  chatPeer.ChatID,
			}
		case DestSupergroup, DestChannel:
			chPeer, ok := resolved.(*tg.InputPeerChannel)
			if !ok || chPeer.AccessHash == 0 {
				return NotificationDestination{}, fmt.Errorf("%w: target bukan %s dengan access hash valid", errNotifyDestTypeMismatch, destType)
			}
			prefix := "sg"
			if destType == DestChannel {
				prefix = "ch"
			}
			dest = NotificationDestination{
				ID:         fmt.Sprintf("%s-%d", prefix, chPeer.ChannelID),
				Type:       destType,
				Label:      label,
				Enabled:    true,
				PeerID:     chPeer.ChannelID,
				AccessHash: chPeer.AccessHash,
			}
		case DestBotPM:
			userPeer, ok := resolved.(*tg.InputPeerUser)
			if !ok || userPeer.AccessHash == 0 {
				return NotificationDestination{}, fmt.Errorf("%w: target bot_pm harus berupa user/bot dengan access hash valid", errNotifyDestTypeMismatch)
			}
			dest = NotificationDestination{
				ID:         fmt.Sprintf("bot-%d", userPeer.UserID),
				Type:       DestBotPM,
				Label:      label,
				Enabled:    true,
				PeerID:     userPeer.UserID,
				AccessHash: userPeer.AccessHash,
			}
		}

		norm, err := NormalizeAndValidateDestination(dest)
		if err != nil {
			return NotificationDestination{}, err
		}

		// Pre-validate delivery permission before saving when MessageService is wired.
		p.maintMu.RLock()
		msgSvcFn := p.msgSvcFunc
		ownerID := p.ownerID
		resolver := p.peerResolver
		ulog := p.userlogSvc
		p.maintMu.RUnlock()
		if msgSvcFn != nil && msgSvcFn() != nil {
			res := deliverMaintenanceNotifications(
				cCtx,
				msgSvcFn(),
				ownerID,
				resolver,
				ulog,
				EventNone,
				[]NotificationDestination{norm},
				"🧪 <b>Verifikasi Tujuan Notifikasi MyXL</b>\n\nChat ini terverifikasi untuk menerima notifikasi Token Maintenance.",
				maintenanceNotifyTimeout,
				maintenanceNotifyTimeout,
			)
			if res.Sent != 1 {
				return NotificationDestination{}, errors.New("gagal mengirim pesan verifikasi ke tujuan (periksa izin posting atau akses bot)")
			}
		}
		return norm, nil

	default:
		return NotificationDestination{}, fmt.Errorf("tipe destination %q tidak dikenal", destType)
	}
}

func maskPeerID(id int64) string {
	if id == 0 {
		return "-"
	}
	s := strconv.FormatInt(id, 10)
	if len(s) <= 4 {
		return "****"
	}
	return "***" + s[len(s)-4:]
}

func formatNotifyEventsSummaryLine(cfg *TokenMaintenanceConfig) string {
	onOff := func(v bool) string {
		if v {
			return "ON"
		}
		return "OFF"
	}
	return fmt.Sprintf("Event: completion=<code>%s</code>, partial_failure=<code>%s</code>, failure=<code>%s</code>, cancelled=<code>%s</code>",
		onOff(cfg.NotifyOnCompletion), onOff(cfg.NotifyOnPartialFailure), onOff(cfg.NotifyOnFailure), onOff(cfg.NotifyOnCancelled))
}

func formatNotifyStatusHTML(cfg *TokenMaintenanceConfig) string {
	var b strings.Builder
	b.WriteString("🔔 <b>Notifikasi MyXL Token Maintenance</b>\n\n")
	b.WriteString("<b>Event:</b>\n")
	fmt.Fprintf(&b, "• <code>completion</code>: <b>%t</b>\n", cfg.NotifyOnCompletion)
	fmt.Fprintf(&b, "• <code>partial_failure</code>: <b>%t</b>\n", cfg.NotifyOnPartialFailure)
	fmt.Fprintf(&b, "• <code>failure</code>: <b>%t</b>\n", cfg.NotifyOnFailure)
	fmt.Fprintf(&b, "• <code>cancelled</code>: <b>%t</b>\n\n", cfg.NotifyOnCancelled)
	b.WriteString(formatNotifyDestinationsHTML(cfg))
	return b.String()
}

func formatNotifyDestinationsHTML(cfg *TokenMaintenanceConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Destinations (%d/%d):</b>\n", len(cfg.Destinations), MaxNotificationDestinations)
	if len(cfg.Destinations) == 0 {
		b.WriteString("• <i>Fallback implicit aktif:</i> <code>owner</code> (Owner PM)")
		return b.String()
	}
	for i, d := range cfg.Destinations {
		state := "OFF"
		if d.Enabled {
			state = "ON"
		}
		peerInfo := ""
		if d.PeerID != 0 {
			peerInfo = " · peer " + maskPeerID(d.PeerID)
		}
		fmt.Fprintf(&b, "%d. <code>%s</code> · <b>%s</b> (%s%s) [%s]\n",
			i+1, html.EscapeString(d.ID), html.EscapeString(d.Label), html.EscapeString(string(d.Type)), peerInfo, state)
	}
	return strings.TrimRight(b.String(), "\n")
}

func notifyCommandUsageHTML() string {
	return "Format <code>.myxl schedule notify</code>:\n\n" +
		"• <code>.myxl schedule notify status</code>\n" +
		"• <code>.myxl schedule notify &lt;completion|partial_failure|failure|cancelled&gt; &lt;on|off&gt;</code>\n" +
		"• <code>.myxl schedule notify destination list</code>\n" +
		"• <code>.myxl schedule notify destination add &lt;type&gt; [target]</code>\n" +
		"• <code>.myxl schedule notify destination remove &lt;id&gt;</code>\n" +
		"• <code>.myxl schedule notify destination enable|disable|test &lt;id&gt;</code>"
}

func notifyDestinationUsageHTML() string {
	return "Format <code>.myxl schedule notify destination</code>:\n\n" +
		"• <code>list</code>\n" +
		"• <code>add &lt;owner|global_log|group|supergroup|bot_pm|channel&gt; [target]</code>\n" +
		"• <code>remove &lt;id&gt;</code>\n" +
		"• <code>enable &lt;id&gt;</code> | <code>disable &lt;id&gt;</code>\n" +
		"• <code>test &lt;id&gt;</code>"
}
