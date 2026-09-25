package pmpermit

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/internal/settings"
)

var _ plugin.MessageEventPlugin = (*Plugin)(nil)
var _ plugin.MessageEventStatePlugin = (*Plugin)(nil)

type Plugin struct {
	svc      *pmpermit.Service
	resolver core.PeerResolver
	settings *settings.Service
}

func New(svc *pmpermit.Service) *Plugin                        { return &Plugin{svc: svc} }
func (p *Plugin) SetResolver(resolver core.PeerResolver)       { p.resolver = resolver }
func (p *Plugin) SetSettingsService(service *settings.Service) { p.settings = service }
func (p *Plugin) Name() string                                 { return "pmpermit" }
func (p *Plugin) Description() string {
	return "Anti-spam shield and private message access control system"
}
func (p *Plugin) Init() error              { return nil }
func (p *Plugin) MessageHookPriority() int { return 10 }

func (p *Plugin) MessageHookInterested(int64) bool {
	return p.svc == nil || p.svc.IsEnabled()
}

func (p *Plugin) MessageHookRouting() core.MessageHookRouting {
	return core.MessageHookRouting{
		Lane: core.MessageHookDecision,
		Interests: []core.MessageHookInterest{
			{Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerPrivate},
			{Directions: core.MessageDirectionOutgoing, Peers: core.MessagePeerPrivate},
		},
	}
}
func (p *Plugin) Service() *pmpermit.Service { return p.svc }

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{Name: "approve", Aliases: []string{"allow"}, Description: "Approve a user for private messaging", Usage: ".approve (in PM, reply, @username, or user_id)", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleApprove},
		{Name: "disapprove", Aliases: []string{"disallow", "da"}, Description: "Revoke PM approval for a user", Usage: ".disapprove (in PM, reply, @username, or user_id)", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleDisapprove},
		{Name: "blockpm", Aliases: []string{"pmblock", "b"}, Description: "Block user from private messaging", Usage: ".blockpm (in PM, reply, @username, or user_id)", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleBlock},
		{Name: "unblockpm", Aliases: []string{"pmunblock"}, Description: "Unblock user from private messaging", Usage: ".unblockpm (in PM, reply, @username, or user_id)", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleUnblock},
		{Name: "listapproved", Aliases: []string{"approvedlist"}, Description: "List approved private message users", Usage: ".listapproved", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleListApproved},
		{Name: "pmpermit", Aliases: []string{"pmguard"}, Description: "PMPermit dashboard, toggle, and inspection", Usage: ".pmpermit [on|off|status|test|list|unblock]", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleToggle},
	}
}

func (p *Plugin) HandleMessageEvent(ctx context.Context, message *core.MessageEnvelope) error {
	if p.svc == nil || !p.svc.IsEnabled() || message == nil {
		return nil
	}
	if message.Outgoing {
		if message.IsCommand || message.CommandName != "" {
			return nil
		}
		if p.svc.IsPMPermitMessage(message.Text) || p.svc.IsBotSent(message.ID) {
			return nil
		}
		if !message.IsPrivate() {
			return nil
		}
		targetID := message.ChatID
		if targetID == 0 || targetID == p.svc.OwnerID() || p.svc.IsSudoID(targetID) {
			return nil
		}
		if p.svc.IsWarnID(targetID, message.ID) {
			return nil
		}
		peer, err := message.Peer.InputPeer()
		if err != nil {
			return nil
		}
		userPeer, ok := peer.(*tg.InputPeerUser)
		if !ok || userPeer.AccessHash == 0 {
			return nil
		}
		return p.svc.AutoApproveOutgoing(ctx, userPeer, targetID)
	}

	if !message.IsPrivate() {
		return nil
	}
	senderID := message.Sender.ID
	if senderID == 0 {
		senderID = message.ChatID
	}
	if senderID == 0 {
		return nil
	}

	var peerInput *tg.InputPeerUser
	if peer, err := message.Peer.InputPeer(); err == nil {
		if userPeer, ok := peer.(*tg.InputPeerUser); ok && userPeer.UserID == senderID && userPeer.AccessHash != 0 {
			peerInput = userPeer
		}
	}
	if peerInput == nil && p.resolver != nil {
		peer, resolvedID, err := p.resolver.ResolveUser(ctx, strconv.FormatInt(senderID, 10))
		if resolved, ok := peer.(*tg.InputPeerUser); err == nil && ok && resolved != nil &&
			resolved.UserID == senderID && resolvedID == senderID && resolved.AccessHash != 0 {
			peerInput = resolved
		}
	}

	actor := pmpermit.PMActor{
		UserID:   senderID,
		IsBot:    message.Sender.IsBot,
		Verified: message.SenderVerified,
		IsSelf:   message.SenderSelf,
	}
	if actor.IsBot || actor.Verified || actor.IsSelf {
		return nil
	}
	if peerInput == nil {
		// Fail closed: an unapproved private message with incomplete peer data
		// must not reach commands/automation merely because access hash is absent.
		return core.ErrInterceptHandled
	}
	handled, err := p.svc.HandleIncomingPM(ctx, peerInput, senderID, actor)
	if err != nil {
		return err
	}
	if handled {
		if message.IsCommand {
			core.MarkMessageHandled(senderID, message.ID)
		}
		if decision := core.GetMessageDecision(ctx); decision != nil {
			decision.SetHandled(true)
			decision.SetSuppressAutomation(true)
			decision.SetSuppressAFK(true)
			decision.SetSuppressFilters(true)
			decision.SetSuppressCommands(true)
		}
		return core.ErrInterceptHandled
	}
	return nil
}
func (p *Plugin) resolveTargetUser(ctx *core.Context) (tg.InputPeerClass, int64) {
	if peer, id, err := ctx.ResolveTargetUser(); err == nil && id != 0 {
		return peer, id
	}
	if u, ok := ctx.PeerID.(*tg.InputPeerUser); ok && u != nil && u.UserID != 0 && u.AccessHash != 0 {
		return u, u.UserID
	}
	return nil, 0
}

func (p *Plugin) handleApprove(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Status("PM Permit service is not configured.")
	}
	peer, target := p.resolveTargetUser(ctx)
	if target == 0 {
		return ctx.Status("Could not determine user. Reply to a message, run inside a PM, or provide user ID / @username.")
	}
	if peer == nil {
		return ctx.Status("Could not resolve a usable Telegram peer for this user.")
	}
	reason := "Approved by owner"
	if len(ctx.Args) > 1 {
		reason = strings.Join(ctx.Args[1:], " ")
	}
	if err := p.svc.ApproveWithPeer(ctx.Ctx, peer, target, reason, 0); err != nil {
		return ctx.Error(fmt.Sprintf("Failed to approve user: %v", err))
	}
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("✅ <b>Approved</b> %s for private messaging.", ctx.DisplayUser(peer, target)), 4*time.Second)
}

func (p *Plugin) handleDisapprove(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Status("PM Permit service is not configured.")
	}
	peer, target := p.resolveTargetUser(ctx)
	if target == 0 {
		return ctx.Status("Could not determine user. Reply to a message, run inside a PM, or provide user ID / @username.")
	}
	if err := p.svc.Disapprove(ctx.Ctx, target); err != nil {
		return ctx.Error(fmt.Sprintf("Failed to revoke approval: %v", err))
	}
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("⚠️ <b>Revoked approval</b> for %s.", ctx.DisplayUser(peer, target)), 4*time.Second)
}

func (p *Plugin) handleBlock(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Status("PM Permit service is not configured.")
	}
	peer, target := p.resolveTargetUser(ctx)
	if target == 0 {
		return ctx.Status("Could not determine user. Reply to a message, run inside a PM, or provide user ID / @username.")
	}
	if peer == nil {
		return ctx.Status("Could not resolve a usable Telegram peer for this user.")
	}
	reason := "Blocked by owner"
	if len(ctx.Args) > 1 {
		reason = strings.Join(ctx.Args[1:], " ")
	}
	if err := p.svc.BlockWithPeer(ctx.Ctx, peer, target, reason); err != nil {
		return ctx.Error(fmt.Sprintf("Failed to block user: %v", err))
	}
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("⛔ <b>Blocked</b> %s from private messaging.", ctx.DisplayUser(peer, target)), 4*time.Second)
}

func (p *Plugin) handleUnblock(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Status("PM Permit service is not configured.")
	}
	peer, target := p.resolveTargetUser(ctx)
	if target == 0 {
		return ctx.Status("Could not determine user. Reply to a message, run inside a PM, or provide user ID / @username.")
	}
	if err := p.svc.Unblock(ctx.Ctx, peer, target); err != nil {
		return ctx.Error(fmt.Sprintf("Failed to unblock user: %v", err))
	}
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("✅ <b>Unblocked</b> %s.", ctx.DisplayUser(peer, target)), 4*time.Second)
}

func (p *Plugin) handleListApproved(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Status("PM Permit service is not configured.")
	}
	return p.renderList(ctx, "approved")
}

func (p *Plugin) renderList(ctx *core.Context, statusFilter string) error {
	var records []*pmpermit.PMPermitRecord
	var err error
	switch statusFilter {
	case "approved":
		records, err = p.svc.ListApproved(ctx.Ctx, 50, 0)
	case "blocked":
		records, err = p.svc.ListBlocked(ctx.Ctx, 50, 0)
	case "pending":
		records, err = p.svc.ListPending(ctx.Ctx, 50, 0)
	default:
		return ctx.Status("Invalid status filter. Use <code>approved</code>, <code>blocked</code>, or <code>pending</code>.")
	}
	if err != nil {
		return ctx.Error(fmt.Sprintf("Failed to list records: %v", err))
	}
	if len(records) == 0 {
		return ctx.EditOrReply(fmt.Sprintf("📋 No <b>%s</b> PM records found.", statusFilter))
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 <b>PM Permit: %s Users (%d)</b>\n\n", strings.ToUpper(statusFilter), len(records)))
	for i, r := range records {
		sb.WriteString(fmt.Sprintf("%d. <code>%d</code>", i+1, r.UserID))
		if r.Reason != "" {
			sb.WriteString(fmt.Sprintf(" — <i>%s</i>", r.Reason))
		}
		sb.WriteString("\n")
		if i >= 40 {
			sb.WriteString(fmt.Sprintf("<i>... and %d more</i>\n", len(records)-40))
			break
		}
	}
	return ctx.Result(sb.String())
}

func (p *Plugin) setEnabled(ctx *core.Context, enabled bool) error {
	if p.svc == nil {
		return fmt.Errorf("PM Permit service is not configured")
	}
	if p.settings != nil {
		value := "false"
		if enabled {
			value = "true"
		}
		return p.settings.Set(ctx.Ctx, settings.ScopeGlobal, 0, "pmpermit", "enabled", value, ctx.SenderID())
	}
	// Standalone/tests without the application settings runtime retain the
	// direct legacy behavior.
	p.svc.SetEnabled(enabled)
	return nil
}

func (p *Plugin) handleToggle(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Status("PM Permit service is not configured.")
	}
	sub := ""
	if len(ctx.Args) > 0 {
		sub = strings.ToLower(ctx.Args[0])
	}
	switch sub {
	case "on", "enable", "true":
		if err := p.setEnabled(ctx, true); err != nil {
			return ctx.Error(fmt.Sprintf("Failed to enable PM Permit: %v", err))
		}
		return ctx.EditOrReply("🛡️ <b>PM Permit</b> is now <b>ENABLED</b>.")
	case "off", "disable", "false":
		if err := p.setEnabled(ctx, false); err != nil {
			return ctx.Error(fmt.Sprintf("Failed to disable PM Permit: %v", err))
		}
		return ctx.Status("<b>PM Permit</b> is now <b>DISABLED</b>.")
	case "unblock":
		if len(ctx.Args) < 2 {
			return ctx.Status("Usage: <code>.pmpermit unblock <user_id / @username / reply></code>")
		}
		subCtx := *ctx
		subCtx.Args = ctx.Args[1:]
		peer, target := p.resolveTargetUser(&subCtx)
		if target == 0 {
			return ctx.Status("Could not determine user to unblock.")
		}
		if err := p.svc.Unblock(ctx.Ctx, peer, target); err != nil {
			return ctx.Error(fmt.Sprintf("Failed to unblock user: %v", err))
		}
		return ctx.EditOrReplyWithDelay(fmt.Sprintf("✅ <b>Unblocked</b> %s.", ctx.DisplayUser(peer, target)), 4*time.Second)
	case "list":
		statusFilter := "approved"
		if len(ctx.Args) > 1 {
			statusFilter = strings.ToLower(ctx.Args[1])
		}
		return p.renderList(ctx, statusFilter)
	case "test":
		pending, approved, blocked, err := p.svc.GetStats(ctx.Ctx)
		if err != nil {
			return ctx.Error(fmt.Sprintf("PM Permit test failed: %v", err))
		}
		return ctx.Success(fmt.Sprintf("<b>PM Permit Self-Test OK</b>\n\n<b>Status:</b> %s\n<b>Max Warns:</b> %d\n\n• <b>Approved:</b> <code>%d</code>\n• <b>Pending:</b> <code>%d</code>\n• <b>Blocked:</b> <code>%d</code>\n\n<b>Commands:</b>\n• <code>.approve</code> / <code>.disapprove</code> / <code>.blockpm</code> / <code>.unblockpm</code>\n• <code>.pmpermit [on|off]</code>\n• <code>.pmpermit list [approved|blocked|pending]</code>\n• <code>.pmpermit test</code>", map[bool]string{true: "ENABLED", false: "DISABLED"}[p.svc.IsEnabled()], p.svc.MaxWarns(), approved, pending, blocked))
	case "status", "":
		statusStr := "❌ DISABLED"
		if p.svc.IsEnabled() {
			statusStr = "🛡️ ENABLED"
		}
		pending, approved, blocked, err := p.svc.GetStats(ctx.Ctx)
		if err != nil {
			return ctx.Error(fmt.Sprintf("Failed to read PM Permit status: %v", err))
		}
		text := fmt.Sprintf("🛡️ <b>PM Permit Dashboard</b>\n\n<b>Status:</b> %s\n<b>Max Warns:</b> <code>%d</code>\n<b>Burst Cooldown:</b> <code>%s</code>\n\n<b>Access Control Records:</b>\n• <b>Approved:</b> <code>%d</code>\n• <b>Pending:</b> <code>%d</code>\n• <b>Blocked:</b> <code>%d</code>\n\n<b>Commands:</b>\n• <code>.approve</code> / <code>.disapprove</code> / <code>.blockpm</code> / <code>.unblockpm</code>\n• <code>.pmpermit [on|off]</code>\n• <code>.pmpermit list [approved|blocked|pending]</code>\n• <code>.pmpermit test</code>", statusStr, p.svc.MaxWarns(), p.svc.WarnCooldown(), approved, pending, blocked)
		return ctx.EditOrReply(text)
	default:
		return ctx.Status("Unknown option. Usage: <code>.pmpermit [on|off|status|test|list|unblock]</code>")
	}
}
