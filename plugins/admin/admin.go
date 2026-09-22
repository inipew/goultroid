package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/services/moderation"
)

var (
	_ execution.CapabilityProvider = (*Plugin)(nil)
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

// Capabilities declares the capabilities provided by this plugin (§4, §28 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "admin",
			Name:        "Admin",
			Description: "Group administration and moderation tools",
			Category:    "Admin",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

func (p *Plugin) Commands() []core.Command {
	adminSurfaces := execution.SurfaceUserbot | execution.SurfaceAssistant
	return []core.Command{
		{
			Name:        "ban",
			Description: "Ban a user from the group",
			Usage:       ".ban <user_id / reply> [reason]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization:  core.MustGroupMutationRequirement(core.GroupMutationBan),
			GroupOnly:   true,
			Surfaces:    adminSurfaces,
			Handler:     p.handleBan,
		},
		{
			Name:        "unban",
			Description: "Unban a user in the group",
			Usage:       ".unban <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization:  core.MustGroupMutationRequirement(core.GroupMutationUnban),
			GroupOnly:   true,
			Surfaces:    adminSurfaces,
			Handler:     p.handleUnban,
		},
		{
			Name:        "kick",
			Description: "Kick a user from the group",
			Usage:       ".kick <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization:  core.MustGroupMutationRequirement(core.GroupMutationKick),
			GroupOnly:   true,
			Surfaces:    adminSurfaces,
			Handler:     p.handleKick,
		},
		{
			Name:        "mute",
			Description: "Mute a user in the group (optional duration, e.g. 10m, 2h, 1d)",
			Usage:       ".mute <user_id / reply> [duration]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization:  core.MustGroupMutationRequirement(core.GroupMutationMute),
			GroupOnly:   true,
			Surfaces:    adminSurfaces,
			Handler:     p.handleMute,
		},
		{
			Name:        "unmute",
			Description: "Unmute a user in the group",
			Usage:       ".unmute <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization:  core.MustGroupMutationRequirement(core.GroupMutationUnmute),
			GroupOnly:   true,
			Surfaces:    adminSurfaces,
			Handler:     p.handleUnmute,
		},
		{
			Name:        "purge",
			Description: "Safely delete messages between replied message and this command (topic-aware)",
			Usage:       "reply to a message with .purge",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization:  core.MustGroupMutationRequirement(core.GroupMutationPurge),
			GroupOnly:   true,
			ReplyOnly:   true,
			Surfaces:    adminSurfaces,
			Handler:     p.handlePurge,
		},
		{
			Name:        "promote",
			Description: "Promote a user to admin with custom title",
			Usage:       ".promote <user_id / reply> [title]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization:  core.MustGroupMutationRequirement(core.GroupMutationPromote),
			GroupOnly:   true,
			Surfaces:    adminSurfaces,
			Handler:     p.handlePromote,
		},
		{
			Name:        "demote",
			Description: "Demote an admin back to regular user",
			Usage:       ".demote <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization:  core.MustGroupMutationRequirement(core.GroupMutationDemote),
			GroupOnly:   true,
			Surfaces:    adminSurfaces,
			Handler:     p.handleDemote,
		},
		{
			Name:        "warn",
			Description: "Add a warning to a user (auto-punishes when threshold reached)",
			Usage:       ".warn <user_id / reply> [reason]",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization:  core.MustGroupMutationRequirement(core.GroupMutationMute),
			GroupOnly:   true,
			Surfaces:    adminSurfaces,
			Handler:     p.handleWarn,
		},
		{
			Name:        "warns",
			Description: "View active warnings for a user",
			Usage:       ".warns <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization: core.GroupAuthorizationRequirement{
				Level: core.GroupAuthorizationAdministrator,
			},
			GroupOnly: true,
			Surfaces:  adminSurfaces,
			Handler:   p.handleWarns,
		},
		{
			Name:        "resetwarns",
			Description: "Reset all warnings for a user",
			Usage:       ".resetwarns <user_id / reply>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization: core.GroupAuthorizationRequirement{
				Level: core.GroupAuthorizationAdministrator,
			},
			GroupOnly: true,
			Surfaces:  adminSurfaces,
			Handler:   p.handleResetWarns,
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
	switch {
	case errors.Is(err, core.ErrGroupMutationTargetProtected):
		return fmt.Sprintf("❌ Gagal %s: target dilindungi oleh hierarki admin Telegram.", action)
	case errors.Is(err, core.ErrGroupMutationDenied),
		errors.Is(err, core.ErrGroupAuthorizationDenied),
		errors.Is(err, core.ErrPermissionDenied):
		return fmt.Sprintf("❌ Gagal %s: Anda atau Assistant bot tidak memiliki hak Telegram yang diperlukan.", action)
	case errors.Is(err, core.ErrUnsupported):
		return "⚠️ Operasi ini tidak didukung untuk tipe grup tersebut."
	case errors.Is(err, core.ErrUnavailable),
		errors.Is(err, core.ErrResourceLimit),
		errors.Is(err, core.ErrRateLimited),
		errors.Is(err, core.ErrTimeout):
		return core.UserMessage(err)
	}

	upper := strings.ToUpper(err.Error())
	switch {
	case strings.Contains(upper, "USER_ADMIN_INVALID"),
		strings.Contains(upper, "USER_CREATOR"):
		return fmt.Sprintf("❌ Gagal %s: target dilindungi oleh hierarki admin Telegram.", action)
	case strings.Contains(upper, "ADMINS_TOO_MUCH"):
		return "❌ Gagal: batas maksimal admin di grup ini telah tercapai."
	case strings.Contains(upper, "CHAT_ADMIN_REQUIRED"),
		strings.Contains(upper, "RIGHT_FORBIDDEN"):
		return fmt.Sprintf("❌ Gagal %s: hak admin Telegram yang diperlukan tidak tersedia.", action)
	}
	return "❌ Operasi admin gagal. Silakan coba lagi."
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
		reason = fmt.Sprintf("\n<b>Reason:</b> %s", core.EscapeHTML(strings.Join(ctx.Args[1:], " ")))
	}

	rawReason := ""
	if len(ctx.Args) > 1 {
		rawReason = strings.TrimSpace(strings.Join(ctx.Args[1:], " "))
	}
	p.publishAdminAction(ctx, "ban", targetID, rawReason)

	return ctx.EditOrReply(fmt.Sprintf("🔨 Banned user %s.%s", ctx.DisplayUser(targetPeer, targetID), reason))
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

	return ctx.EditOrReply(fmt.Sprintf("✅ Unbanned user %s.", ctx.DisplayUser(targetPeer, targetID)))
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

	return ctx.EditOrReply(fmt.Sprintf("👢 Kicked user %s.", ctx.DisplayUser(targetPeer, targetID)))
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

	return ctx.EditOrReply(fmt.Sprintf("🔇 Muted user %s%s.", ctx.DisplayUser(targetPeer, targetID), durStr))
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

	return ctx.EditOrReply(fmt.Sprintf("🔊 Unmuted user %s.", ctx.DisplayUser(targetPeer, targetID)))
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
		titleStr = fmt.Sprintf(" with title <i>%s</i>", core.EscapeHTML(title))
	}

	return ctx.EditOrReply(fmt.Sprintf("👑 Promoted user %s%s to admin.", ctx.DisplayUser(targetPeer, targetID), titleStr))
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

	return ctx.EditOrReply(fmt.Sprintf("📉 Demoted admin %s to normal user.", ctx.DisplayUser(targetPeer, targetID)))
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

func validateAssistantWarningTargetAt(
	resolveCtx context.Context,
	ctx *core.Context,
	targetID int64,
) error {
	if ctx == nil || ctx.Source != core.ExecutionAssistant {
		return nil
	}
	if targetID <= 0 {
		return core.ErrInvalidArgs
	}
	if ctx.Perms != nil && (ctx.Perms.IsOwner(targetID) || ctx.Perms.IsSudo(targetID)) {
		return core.ErrGroupMutationTargetProtected
	}
	if ctx.Chat == nil || !ctx.IsManagerGroup() || ctx.GroupRoles == nil {
		return fmt.Errorf("%w: warning target role verification unavailable", core.ErrUnavailable)
	}
	if resolveCtx == nil {
		resolveCtx = ctx.Ctx
	}
	snapshot, err := ctx.GroupRoles.ResolveGroupRoleFresh(resolveCtx, core.GroupRoleRequest{
		ChatID: ctx.Chat.ID,
		Kind:   ctx.Chat.Kind(),
		Peer:   ctx.PeerID,
		UserID: targetID,
	})
	if err != nil {
		return err
	}
	if !snapshot.Principal.Verified || snapshot.Principal.UserID != targetID {
		return fmt.Errorf("%w: warning target role is not verified", core.ErrUnavailable)
	}
	switch snapshot.Principal.Role {
	case core.GroupActorRoleAdministrator, core.GroupActorRoleCreator:
		return core.ErrGroupMutationTargetProtected
	default:
		return nil
	}
}

func validateAssistantWarningTarget(ctx *core.Context, targetID int64) error {
	if ctx == nil {
		return core.ErrInvalidArgs
	}
	return validateAssistantWarningTargetAt(ctx.Ctx, ctx, targetID)
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
	if err := validateAssistantWarningTarget(ctx, targetID); err != nil {
		_ = ctx.EditOrReply(formatAdminError("warn user", err))
		return err
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

	var res *moderation.WarnResult
	if ctx.Source == core.ExecutionAssistant {
		contextual, ok := p.moderator.(interface {
			WarnWithServiceGuarded(
				context.Context,
				core.TelegramServicer,
				tg.InputPeerClass,
				tg.InputPeerClass,
				int64,
				int64,
				string,
				int64,
				int,
				string,
				func(context.Context) error,
			) (*moderation.WarnResult, error)
		})
		if !ok {
			err = fmt.Errorf("%w: guarded contextual moderation service is unavailable", core.ErrUnavailable)
		} else {
			res, err = contextual.WarnWithServiceGuarded(
				ctx.Ctx,
				ctx.Svc,
				ctx.PeerID,
				targetPeer,
				chatID,
				targetID,
				reason,
				warnedBy,
				3,
				moderation.ActionMute,
				func(guardCtx context.Context) error {
					return validateAssistantWarningTargetAt(guardCtx, ctx, targetID)
				},
			)
		}
	} else {
		res, err = p.moderator.Warn(
			ctx.Ctx,
			ctx.PeerID,
			targetPeer,
			chatID,
			targetID,
			reason,
			warnedBy,
			3,
			moderation.ActionMute,
		)
	}
	if err != nil {
		_ = ctx.EditOrReply(formatAdminError("warn user", err))
		return err
	}

	targetStr := ctx.DisplayUser(targetPeer, targetID)
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

	targetPeer, targetID, err := ctx.ResolveTargetUser()
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

	targetStr := ctx.DisplayUser(targetPeer, targetID)
	if len(records) == 0 {
		return ctx.EditOrReply(fmt.Sprintf("User <b>%s</b> has 0 active warnings.", targetStr))
	}

	var sb strings.Builder
	sb.WriteString(ctx.T("admin.warns_count", targetStr, len(records)))
	sb.WriteString("\n\n<b>Recent warnings:</b>\n")
	for i, r := range records {
		sb.WriteString(fmt.Sprintf("%d. <i>%s</i> (by <code>%d</code>)\n", i+1, core.EscapeHTML(r.Reason), r.WarnedBy))
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

	targetPeer, targetID, err := ctx.ResolveTargetUser()
	if err != nil {
		_ = ctx.EditOrReply("⚠️ " + err.Error())
		return err
	}

	chatID := ctx.ChatID()
	if err := p.moderator.ResetWarnings(ctx.Ctx, chatID, targetID); err != nil {
		_ = ctx.EditOrReply(formatAdminError("reset warnings", err))
		return err
	}

	targetStr := ctx.DisplayUser(targetPeer, targetID)
	return ctx.EditOrReply(ctx.T("admin.warns_cleared", targetStr))
}

func (p *Plugin) publishAdminAction(ctx *core.Context, action string, targetID int64, reason string) {
	if ctx == nil || ctx.EventBus == nil ||
		!ctx.EventBus.HasSubscribersAtPriority(core.EventTypeAdminAction, core.PriorityHigh) {
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
