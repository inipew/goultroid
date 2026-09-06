package admin

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/moderation"
)

// Plugin provides group administration and moderation commands.
type Plugin struct {
	moderator moderation.Moderator
}

// New creates a new admin plugin instance.
func New(mod ...moderation.Moderator) *Plugin {
	p := &Plugin{}
	if len(mod) > 0 {
		p.moderator = mod[0]
	}
	return p
}

// SetModerator configures the moderation service.
func (p *Plugin) SetModerator(mod moderation.Moderator) {
	p.moderator = mod
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
		{
			Name:        "warn",
			Description: "Add a warning to a user (auto-punishes when threshold reached)",
			Usage:       ".warn <user_id / reply> [reason]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleWarn,
		},
		{
			Name:        "warns",
			Description: "View active warnings for a user",
			Usage:       ".warns <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleWarns,
		},
		{
			Name:        "resetwarns",
			Description: "Reset all warnings for a user",
			Usage:       ".resetwarns <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleResetWarns,
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
		_ = ctx.EditOrReply("⚠️ Fitur ban hanya dapat digunakan di grup atau supergroup.")
		return core.ErrUnsupported
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.EditOrReply("⚠️ Cannot ban the owner!")
		return errors.New("cannot ban owner")
	}

	if err := ctx.Ban(targetPeer, 0); err != nil {
		_ = ctx.EditOrReply(formatAdminError("ban user", err))
		return err
	}

	reason := ""
	if len(ctx.Args) > 1 {
		reason = fmt.Sprintf("\n<b>Reason:</b> %s", strings.Join(ctx.Args[1:], " "))
	}

	rawReason := ""
	if len(ctx.Args) > 1 {
		rawReason = strings.TrimSpace(strings.Join(ctx.Args[1:], " "))
	}
	p.publishAdminAction(ctx, "ban", targetID, rawReason)

	return ctx.EditOrReply(fmt.Sprintf("🔨 Banned user <code>%d</code>.%s", targetID, reason))
}

func (p *Plugin) handleUnban(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.EditOrReply("⚠️ Fitur unban hanya dapat digunakan di grup or supergroup.")
		return core.ErrUnsupported
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	if err := ctx.Unban(targetPeer); err != nil {
		_ = ctx.EditOrReply(formatAdminError("unban user", err))
		return err
	}

	p.publishAdminAction(ctx, "unban", targetID, "")

	return ctx.EditOrReply(fmt.Sprintf("✅ Unbanned user <code>%d</code>.", targetID))
}

func (p *Plugin) handleKick(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.EditOrReply("⚠️ Fitur kick hanya dapat digunakan in grup or supergroup.")
		return core.ErrUnsupported
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.EditOrReply("⚠️ Cannot kick the owner!")
		return errors.New("cannot kick owner")
	}

	if err := ctx.Kick(targetPeer); err != nil {
		_ = ctx.EditOrReply(formatAdminError("kick user", err))
		return err
	}

	kickReason := ""
	if len(ctx.Args) > 1 {
		kickReason = strings.TrimSpace(strings.Join(ctx.Args[1:], " "))
	}
	p.publishAdminAction(ctx, "kick", targetID, kickReason)

	return ctx.EditOrReply(fmt.Sprintf("👢 Kicked user <code>%d</code>.", targetID))
}

func (p *Plugin) handleMute(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.EditOrReply("⚠️ Fitur mute hanya dapat digunakan in grup or supergroup.")
		return core.ErrUnsupported
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.EditOrReply("⚠️ Cannot mute the owner!")
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
		_ = ctx.EditOrReply(formatAdminError("mute user", err))
		return err
	}

	p.publishAdminAction(ctx, "mute", targetID, durStr)

	return ctx.EditOrReply(fmt.Sprintf("🔇 Muted user <code>%d</code>%s.", targetID, durStr))
}

func (p *Plugin) handleUnmute(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.EditOrReply("⚠️ Fitur unmute hanya dapat digunakan in grup or supergroup.")
		return core.ErrUnsupported
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	if err := ctx.Unmute(targetPeer); err != nil {
		_ = ctx.EditOrReply(formatAdminError("unmute user", err))
		return err
	}

	p.publishAdminAction(ctx, "unmute", targetID, "")

	return ctx.EditOrReply(fmt.Sprintf("🔊 Unmuted user <code>%d</code>.", targetID))
}

func (p *Plugin) handlePurge(ctx *core.Context) error {
	if ctx.Message == nil || ctx.Message.ReplyToID == 0 {
		_ = ctx.EditOrReply("⚠️ Harap reply ke pesan awal yang ingin di-purge.")
		return core.ErrReplyRequired
	}

	count, err := ctx.Purge()
	if err != nil {
		_ = ctx.EditOrReply(formatAdminError("purge pesan", err))
		return err
	}

	topicMsg := ""
	if ctx.TopicID() > 0 {
		topicMsg = fmt.Sprintf(" in topic <code>%d</code>", ctx.TopicID())
	}

	// Purge is destructive and must consume its trigger regardless of whether
	// the command was emitted as an incoming bot update or as an outgoing
	// userbot command. Do not use EditOrReply here: outgoing commands are
	// normally edited in place, which would leave the purge trigger behind.
	// The confirmation notification is self-destructed after 4 seconds to leave
	// the chat cleanly purged without leftover bot responses.
	return ctx.ReplyAndDeleteWithDelay(
		fmt.Sprintf("🗑️ <b>Purged %d messages successfully%s!</b>", count, topicMsg),
		4*time.Second,
	)
}

func (p *Plugin) handlePromote(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.EditOrReply("⚠️ Fitur promote hanya dapat digunakan in grup or supergroup.")
		return core.ErrUnsupported
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
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
		_ = ctx.EditOrReply(formatAdminError("promote user", err))
		return err
	}

	p.publishAdminAction(ctx, "promote", targetID, title)

	titleStr := ""
	if title != "" {
		titleStr = fmt.Sprintf(" with title <i>%s</i>", title)
	}

	return ctx.EditOrReply(fmt.Sprintf("👑 Promoted user <code>%d</code>%s to admin.", targetID, titleStr))
}

func (p *Plugin) handleDemote(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.EditOrReply("⚠️ Fitur demote hanya dapat digunakan in grup or supergroup.")
		return core.ErrUnsupported
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.EditOrReply("⚠️ Cannot demote the owner!")
		return errors.New("cannot demote owner")
	}

	if err := ctx.Demote(targetPeer); err != nil {
		_ = ctx.EditOrReply(formatAdminError("demote user", err))
		return err
	}

	p.publishAdminAction(ctx, "demote", targetID, "")

	return ctx.EditOrReply(fmt.Sprintf("📉 Demoted admin <code>%d</code> to normal user.", targetID))
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

func isNumeric(s string) bool {
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

func (p *Plugin) handleWarn(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.EditOrReply("⚠️ Fitur warn hanya dapat digunakan in grup or supergroup.")
		return core.ErrUnsupported
	}
	if p.moderator == nil {
		_ = ctx.EditOrReply("⚠️ Moderation service is not configured.")
		return fmt.Errorf("%w: moderation service is nil", core.ErrUnavailable)
	}

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	if ctx.Perms != nil && ctx.Perms.IsOwner(targetID) {
		_ = ctx.EditOrReply("⚠️ Cannot warn the owner!")
		return errors.New("cannot warn owner")
	}

	reason := "No reason provided"
	if len(ctx.Args) > 0 {
		if len(ctx.Args) > 1 && (strings.HasPrefix(ctx.Args[0], "@") || isNumeric(ctx.Args[0])) {
			reason = strings.Join(ctx.Args[1:], " ")
		} else if !strings.HasPrefix(ctx.Args[0], "@") && !isNumeric(ctx.Args[0]) {
			reason = strings.Join(ctx.Args, " ")
		}
	}

	chatID := ctx.ChatID()
	warnedBy := ctx.SenderID()

	res, err := p.moderator.Warn(ctx.Ctx, ctx.PeerID, targetPeer, chatID, targetID, reason, warnedBy, 3, moderation.ActionMute)
	if err != nil {
		_ = ctx.EditOrReply(formatAdminError("warn user", err))
		return err
	}

	targetStr := fmt.Sprintf("%d", targetID)
	text := ctx.T("admin.warned", targetStr, res.CurrentCount, res.Threshold, reason)
	if res.ActionTaken != moderation.ActionNone {
		text += "\n" + ctx.T("admin.warn_threshold_reached", targetStr, res.Threshold, res.ActionTaken)
	}

	return ctx.EditOrReply(text)
}

func (p *Plugin) handleWarns(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.EditOrReply("⚠️ Fitur warns hanya dapat digunakan in grup or supergroup.")
		return core.ErrUnsupported
	}
	if p.moderator == nil {
		_ = ctx.EditOrReply("⚠️ Moderation service is not configured.")
		return fmt.Errorf("%w: moderation service is nil", core.ErrUnavailable)
	}

	_, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	chatID := ctx.ChatID()
	records, err := p.moderator.GetWarnings(ctx.Ctx, chatID, targetID)
	if err != nil {
		_ = ctx.EditOrReply(formatAdminError("get warnings", err))
		return err
	}

	targetStr := fmt.Sprintf("%d", targetID)
	if len(records) == 0 {
		return ctx.EditOrReply(fmt.Sprintf("User <b>%s</b> has 0 active warnings.", targetStr))
	}

	var sb strings.Builder
	sb.WriteString(ctx.T("admin.warns_count", targetStr, len(records)))
	sb.WriteString("\n\n<b>Recent warnings:</b>\n")
	for i, r := range records {
		sb.WriteString(fmt.Sprintf("%d. <i>%s</i> (by <code>%d</code>)\n", i+1, r.Reason, r.WarnedBy))
	}

	return ctx.EditOrReply(sb.String())
}

func (p *Plugin) handleResetWarns(ctx *core.Context) error {
	if isPrivateOrUnsupported(ctx) {
		_ = ctx.EditOrReply("⚠️ Fitur resetwarns hanya dapat digunakan in grup or supergroup.")
		return core.ErrUnsupported
	}
	if p.moderator == nil {
		_ = ctx.EditOrReply("⚠️ Moderation service is not configured.")
		return fmt.Errorf("%w: moderation service is nil", core.ErrUnavailable)
	}

	_, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	chatID := ctx.ChatID()
	if err := p.moderator.ResetWarnings(ctx.Ctx, chatID, targetID); err != nil {
		_ = ctx.EditOrReply(formatAdminError("reset warnings", err))
		return err
	}

	targetStr := fmt.Sprintf("%d", targetID)
	return ctx.EditOrReply(ctx.T("admin.warns_cleared", targetStr))
}

func (p *Plugin) publishAdminAction(ctx *core.Context, action string, targetID int64, reason string) {
	if ctx == nil || ctx.EventBus == nil {
		return
	}
	var chatID int64
	var chatTitle string
	if ctx.Chat != nil {
		chatID = ctx.Chat.ID
		chatTitle = ctx.Chat.Title
	} else if ctx.PeerID != nil {
		switch peer := ctx.PeerID.(type) {
		case *tg.InputPeerChannel:
			chatID = peer.ChannelID
		case *tg.InputPeerChat:
			chatID = peer.ChatID
		}
	}

	adminID := ctx.SenderID()
	if adminID == 0 && ctx.Principal != nil {
		adminID = ctx.Principal.UserID
	}

	ctx.EventBus.Publish(&core.AdminActionEvent{
		At:        time.Now(),
		Action:    action,
		ChatID:    chatID,
		ChatTitle: chatTitle,
		TargetID:  targetID,
		ActorID:   adminID,
		Reason:    reason,
		Success:   true,
	})
}

