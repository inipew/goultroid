package admin

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides group administration and moderation commands.
type Plugin struct{}

// New creates a new admin plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) Name() string {
	return "admin"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "ban",
			Description: "Ban a user from the group",
			Usage:       ".ban <user_id / reply> [reason]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleBan,
		},
		{
			Name:        "unban",
			Description: "Unban a user in the group",
			Usage:       ".unban <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleUnban,
		},
		{
			Name:        "kick",
			Description: "Kick a user from the group",
			Usage:       ".kick <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleKick,
		},
		{
			Name:        "mute",
			Description: "Mute a user in the group (optional duration, e.g. 10m, 2h, 1d)",
			Usage:       ".mute <user_id / reply> [duration]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleMute,
		},
		{
			Name:        "unmute",
			Description: "Unmute a user in the group",
			Usage:       ".unmute <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleUnmute,
		},
		{
			Name:        "purge",
			Description: "Safely delete messages between replied message and this command (topic-aware)",
			Usage:       "reply to a message with .purge",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			ReplyOnly:   true,
			Handler:     p.handlePurge,
		},
		{
			Name:        "promote",
			Description: "Promote a user to admin with custom title",
			Usage:       ".promote <user_id / reply> [title]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handlePromote,
		},
		{
			Name:        "demote",
			Description: "Demote an admin back to regular user",
			Usage:       ".demote <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleDemote,
		},
	}
}

func isPrivateOrUnsupported(ctx *core.Context) bool {
	if ctx.Chat != nil && ctx.Chat.Type == "private" {
		return true
	}
	if _, ok := ctx.PeerID.(*tg.InputPeerUser); ok {
		return true
	}
	if _, ok := ctx.PeerID.(*tg.InputPeerSelf); ok {
		return true
	}
	return false
}

func formatAdminError(action string, err error) string {
	if errors.Is(err, core.ErrPermissionDenied) || strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
		return fmt.Sprintf("❌ Gagal %s: Anda/bot harus menjadi Admin dengan hak yang sesuai di grup ini.", action)
	}
	if strings.Contains(err.Error(), "USER_ADMIN_INVALID") {
		return fmt.Sprintf("❌ Gagal %s: Target adalah admin atau memiliki hak lebih tinggi.", action)
	}
	if strings.Contains(err.Error(), "ADMINS_TOO_MUCH") {
		return "❌ Gagal: Batas maksimal admin di grup ini telah tercapai."
	}
	if errors.Is(err, core.ErrUnsupported) {
		return "⚠️ Fitur ini hanya didukung pada Supergroup."
	}
	return fmt.Sprintf("❌ Failed to %s: %v", action, err)
}

func (p *Plugin) handleBan(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.Reply("⚠️ Fitur ban hanya dapat digunakan di grup atau supergroup.")
		return nil
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.Reply("⚠️ Cannot ban the owner!")
		return errors.New("cannot ban owner")
	}

	if err := ctx.Ban(targetPeer, 0); err != nil {
		_ = ctx.Reply(formatAdminError("ban user", err))
		return err
	}

	reason := ""
	if len(ctx.Args) > 1 {
		reason = fmt.Sprintf("\n<b>Reason:</b> %s", strings.Join(ctx.Args[1:], " "))
	}

	return ctx.Reply(fmt.Sprintf("🔨 Banned user <code>%d</code>.%s", targetID, reason))
}

func (p *Plugin) handleUnban(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.Reply("⚠️ Fitur unban hanya dapat digunakan di grup atau supergroup.")
		return nil
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if err := ctx.Unban(targetPeer); err != nil {
		_ = ctx.Reply(formatAdminError("unban user", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("✅ Unbanned user <code>%d</code>.", targetID))
}

func (p *Plugin) handleKick(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.Reply("⚠️ Fitur kick hanya dapat digunakan di grup atau supergroup.")
		return nil
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.Reply("⚠️ Cannot kick the owner!")
		return errors.New("cannot kick owner")
	}

	if err := ctx.Kick(targetPeer); err != nil {
		_ = ctx.Reply(formatAdminError("kick user", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("👢 Kicked user <code>%d</code>.", targetID))
}

func (p *Plugin) handleMute(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.Reply("⚠️ Fitur mute hanya dapat digunakan di grup atau supergroup.")
		return nil
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.Reply("⚠️ Cannot mute the owner!")
		return errors.New("cannot mute owner")
	}

	var untilDate int
	durStr := ""

	// Check for duration in args
	for _, arg := range ctx.Args {
		// skip numeric targetID arg
		if arg == strconv.FormatInt(targetID, 10) {
			continue
		}
		if d, err := parseDuration(arg); err == nil && d > 0 {
			untilDate = int(time.Now().Add(d).Unix())
			durStr = fmt.Sprintf(" for <code>%s</code>", d.String())
			break
		}
	}

	if err := ctx.Mute(targetPeer, untilDate); err != nil {
		_ = ctx.Reply(formatAdminError("mute user", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🔇 Muted user <code>%d</code>%s.", targetID, durStr))
}

func (p *Plugin) handleUnmute(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.Reply("⚠️ Fitur unmute hanya dapat digunakan di grup atau supergroup.")
		return nil
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if err := ctx.Unmute(targetPeer); err != nil {
		_ = ctx.Reply(formatAdminError("unmute user", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🔊 Unmuted user <code>%d</code>.", targetID))
}

func (p *Plugin) handlePurge(ctx *core.Context) error {
	if ctx.Message == nil || ctx.Message.ReplyToID == 0 {
		_ = ctx.Reply("⚠️ Harap reply ke pesan awal yang ingin di-purge.")
		return core.ErrReplyRequired
	}

	count, err := ctx.Purge()
	if err != nil {
		_ = ctx.Reply(formatAdminError("purge pesan", err))
		return err
	}

	topicMsg := ""
	if ctx.TopicID() > 0 {
		topicMsg = fmt.Sprintf(" in topic <code>%d</code>", ctx.TopicID())
	}

	return ctx.Reply(fmt.Sprintf("🗑️ <b>Purged %d messages successfully%s!</b>", count, topicMsg))
}

func (p *Plugin) handlePromote(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.Reply("⚠️ Fitur promote hanya dapat digunakan di grup atau supergroup.")
		return nil
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	title := ""
	if len(ctx.Args) > 0 {
		if _, parseErr := strconv.ParseInt(ctx.Args[0], 10, 64); parseErr == nil {
			if len(ctx.Args) > 1 {
				title = strings.TrimSpace(strings.Join(ctx.Args[1:], " "))
			}
		} else {
			title = strings.TrimSpace(strings.Join(ctx.Args, " "))
		}
	}

	if err := ctx.Promote(targetPeer, title); err != nil {
		_ = ctx.Reply(formatAdminError("promote user", err))
		return err
	}

	titleStr := ""
	if title != "" {
		titleStr = fmt.Sprintf(" with title <i>%s</i>", title)
	}

	return ctx.Reply(fmt.Sprintf("👑 Promoted user <code>%d</code>%s to admin.", targetID, titleStr))
}

func (p *Plugin) handleDemote(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.Reply("⚠️ Fitur demote hanya dapat digunakan di grup atau supergroup.")
		return nil
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.Reply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.Reply("⚠️ Cannot demote the owner!")
		return errors.New("cannot demote owner")
	}

	if err := ctx.Demote(targetPeer); err != nil {
		_ = ctx.Reply(formatAdminError("demote user", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("📉 Demoted admin <code>%d</code> to normal user.", targetID))
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
