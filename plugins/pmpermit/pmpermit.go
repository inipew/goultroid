package pmpermit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/pmpermit"
)

var _ plugin.MessageHookPlugin = (*Plugin)(nil)

type Plugin struct {
	svc *pmpermit.Service
}

func New(svc *pmpermit.Service) *Plugin {
	return &Plugin{svc: svc}
}

func (p *Plugin) Name() string {
	return "pmpermit"
}

func (p *Plugin) Description() string {
	return "Anti-spam shield and private message access control system"
}

func (p *Plugin) Init() error {
	return nil
}

// MessageHookPriority returns priority for the message hook (Security = 10).
func (p *Plugin) MessageHookPriority() int {
	return 10
}

func (p *Plugin) Service() *pmpermit.Service {
	return p.svc
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "approve",
			Aliases:     []string{"allow"},
			Description: "Approve a user for private messaging",
			Usage:       ".approve (in PM, reply, @username, or user_id)",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleApprove,
		},
		{
			Name:        "disapprove",
			Aliases:     []string{"disallow", "da"},
			Description: "Revoke PM approval for a user",
			Usage:       ".disapprove (in PM, reply, @username, or user_id)",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleDisapprove,
		},
		{
			Name:        "blockpm",
			Aliases:     []string{"pmblock", "b"},
			Description: "Block user from private messaging",
			Usage:       ".blockpm (in PM, reply, @username, or user_id)",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleBlock,
		},
		{
			Name:        "unblockpm",
			Aliases:     []string{"pmunblock"},
			Description: "Unblock user from private messaging",
			Usage:       ".unblockpm (in PM, reply, @username, or user_id)",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleUnblock,
		},
		{
			Name:        "listapproved",
			Aliases:     []string{"approvedlist"},
			Description: "List approved private message users",
			Usage:       ".listapproved",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleListApproved,
		},
		{
			Name:        "pmpermit",
			Aliases:     []string{"pmguard"},
			Description: "PMPermit dashboard, toggle, and inspection",
			Usage:       ".pmpermit [on|off|status|test|list|unblock]",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleToggle,
		},
	}
}

func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if p.svc == nil || !p.svc.IsEnabled() || msg == nil {
		return nil
	}

	if msg.Out {
		// 1. NEVER auto-approve if this is a command (e.g. .disapprove, .blockpm, .a, etc.)
		if isCommand || cmdName != "" || strings.HasPrefix(msg.Message, ".") {
			return nil
		}
		// 2. NEVER auto-approve if this is a bot-generated warning or notification message
		if p.svc.IsPMPermitMessage(msg.Message) {
			return nil
		}
		// 3. NEVER auto-approve if this outgoing message was dispatched programmatically by GoUltroid
		// (e.g. Scheduler, Broadcast, Addon, AI, Downloader notifications)
		if p.svc.IsBotSent(msg.ID) {
			return nil
		}

		// Auto-allow when owner legitimately initiates PM to an unapproved/blocked user
		if peerUser, ok := msg.PeerID.(*tg.PeerUser); ok && peerUser != nil {
			targetID := peerUser.UserID
			// Ignore if this message ID matches a recorded warning message
			if p.svc.IsWarnID(targetID, msg.ID) {
				return nil
			}
			// Avoid self-approve
			if targetID != 0 && targetID != p.svc.OwnerID() && !p.svc.IsSudoID(targetID) {
				peer := tg.InputPeerClass(&tg.InputPeerUser{UserID: targetID})
				if u, ok := e.Users[targetID]; ok {
					peer = &tg.InputPeerUser{UserID: targetID, AccessHash: u.AccessHash}
				}
				_ = p.svc.AutoApproveOutgoing(ctx, peer, targetID)
			}
		}
		return nil
	}

	peerUser, ok := msg.PeerID.(*tg.PeerUser)
	if !ok || peerUser == nil {
		return nil
	}
	senderID := peerUser.UserID
	if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			senderID = u.UserID
		}
	}

	peerInput := tg.InputPeerClass(&tg.InputPeerUser{UserID: senderID})
	var actor pmpermit.PMActor
	actor.UserID = senderID
	if u, ok := e.Users[senderID]; ok {
		peerInput = &tg.InputPeerUser{UserID: senderID, AccessHash: u.AccessHash}
		actor.IsBot = u.Bot
		actor.Verified = u.Verified
		actor.IsSelf = u.Self
	}

	handled, err := p.svc.HandleIncomingPM(ctx, peerInput, senderID, actor)
	if err != nil {
		return err
	}
	if handled {
		if isCommand {
			core.MarkMessageHandled(senderID, msg.ID)
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
	if u, ok := ctx.PeerID.(*tg.InputPeerUser); ok && u != nil && u.UserID != 0 {
		return u, u.UserID
	}
	return nil, 0
}

func (p *Plugin) handleApprove(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ PM Permit service is not configured.")
	}
	peer, target := p.resolveTargetUser(ctx)
	if target == 0 {
		return ctx.EditOrReply("⚠️ Could not determine user. Reply to a message, run inside a PM, or provide user ID / @username.")
	}
	reason := "Approved by owner"
	if len(ctx.Args) > 1 {
		reason = strings.Join(ctx.Args[1:], " ")
	}
	if peer == nil {
		peer = &tg.InputPeerUser{UserID: target}
	}
	if err := p.svc.Approve(ctx.Ctx, target, reason, 0); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to approve user: %v", err))
	}
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("✅ <b>Approved</b> user <code>%d</code> for private messaging.", target), 4*time.Second)
}

func (p *Plugin) handleDisapprove(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ PM Permit service is not configured.")
	}
	_, target := p.resolveTargetUser(ctx)
	if target == 0 {
		return ctx.EditOrReply("⚠️ Could not determine user. Reply to a message, run inside a PM, or provide user ID / @username.")
	}
	if err := p.svc.Disapprove(ctx.Ctx, target); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to revoke approval: %v", err))
	}
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("⚠️ <b>Revoked approval</b> for user <code>%d</code>.", target), 4*time.Second)
}

func (p *Plugin) handleBlock(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ PM Permit service is not configured.")
	}
	peer, target := p.resolveTargetUser(ctx)
	if target == 0 {
		return ctx.EditOrReply("⚠️ Could not determine user. Reply to a message, run inside a PM, or provide user ID / @username.")
	}
	reason := "Blocked by owner"
	if len(ctx.Args) > 1 {
		reason = strings.Join(ctx.Args[1:], " ")
	}
	if peer == nil {
		peer = &tg.InputPeerUser{UserID: target}
	}
	if err := p.svc.BlockWithPeer(ctx.Ctx, peer, target, reason); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to block user: %v", err))
	}
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("⛔ <b>Blocked</b> user <code>%d</code> from private messaging.", target), 4*time.Second)
}

func (p *Plugin) handleUnblock(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ PM Permit service is not configured.")
	}
	peer, target := p.resolveTargetUser(ctx)
	if target == 0 {
		return ctx.EditOrReply("⚠️ Could not determine user to unblock. Reply to a message, run inside a PM, or provide user ID / @username.")
	}
	if err := p.svc.Unblock(ctx.Ctx, peer, target); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to unblock user: %v", err))
	}
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("✅ <b>Unblocked</b> user <code>%d</code>.", target), 4*time.Second)
}

func (p *Plugin) handleListApproved(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ PM Permit service is not configured.")
	}
	return p.renderList(ctx, "approved")
}

func (p *Plugin) renderList(ctx *core.Context, statusFilter string) error {
	var records []*database.PMPermitRecord
	var err error
	switch statusFilter {
	case "approved":
		records, err = p.svc.ListApproved(ctx.Ctx, 50, 0)
	case "blocked":
		records, err = p.svc.ListBlocked(ctx.Ctx, 50, 0)
	case "pending":
		records, err = p.svc.ListPending(ctx.Ctx, 50, 0)
	default:
		return ctx.EditOrReply("⚠️ Invalid status filter. Use <code>approved</code>, <code>blocked</code>, or <code>pending</code>.")
	}
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to list records: %v", err))
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
	return ctx.EditOrReply(sb.String())
}

func (p *Plugin) handleToggle(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ PM Permit service is not configured.")
	}

	sub := ""
	if len(ctx.Args) > 0 {
		sub = strings.ToLower(ctx.Args[0])
	}

	switch sub {
	case "on", "enable", "true":
		p.svc.SetEnabled(true)
		return ctx.EditOrReply("🛡️ <b>PM Permit</b> is now <b>ENABLED</b>.")
	case "off", "disable", "false":
		p.svc.SetEnabled(false)
		return ctx.EditOrReply("⚠️ <b>PM Permit</b> is now <b>DISABLED</b>.")
	case "unblock":
		if len(ctx.Args) < 2 {
			return ctx.EditOrReply("⚠️ Usage: <code>.pmpermit unblock <user_id / @username / reply></code>")
		}
		subCtx := *ctx
		subCtx.Args = ctx.Args[1:]
		peer, target := p.resolveTargetUser(&subCtx)
		if target == 0 {
			return ctx.EditOrReply("⚠️ Could not determine user to unblock.")
		}
		if err := p.svc.Unblock(ctx.Ctx, peer, target); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to unblock user: %v", err))
		}
		return ctx.EditOrReplyWithDelay(fmt.Sprintf("✅ <b>Unblocked</b> user <code>%d</code>.", target), 4*time.Second)
	case "list":
		statusFilter := "approved"
		if len(ctx.Args) > 1 {
			statusFilter = strings.ToLower(ctx.Args[1])
		}
		return p.renderList(ctx, statusFilter)
	case "test":
		pending, approved, blocked, err := p.svc.GetStats(ctx.Ctx)
		if err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ PM Permit test failed: %v", err))
		}
		return ctx.EditOrReply(fmt.Sprintf("✅ <b>PM Permit Self-Test OK</b>\n\n• <b>Shield Active:</b> %t\n• <b>Database:</b> Connected\n• <b>Active Records:</b> Pending: <code>%d</code> | Approved: <code>%d</code> | Blocked: <code>%d</code>",
			p.svc.IsEnabled(), pending, approved, blocked))
	case "status", "":
		pending, approved, blocked, _ := p.svc.GetStats(ctx.Ctx)
		statusStr := "🟢 ENABLED"
		if !p.svc.IsEnabled() {
			statusStr = "🔴 DISABLED"
		}
		text := fmt.Sprintf("🛡️ <b>PM Permit Dashboard</b>\n\n"+
			"• <b>Status:</b> %s\n"+
			"• <b>Max Warnings:</b> <code>%d</code>\n"+
			"• <b>Burst Cooldown:</b> <code>2.5s</code>\n\n"+
			"<b>Access Control Records:</b>\n"+
			"• <b>Approved:</b> <code>%d</code>\n"+
			"• <b>Pending:</b> <code>%d</code>\n"+
			"• <b>Blocked:</b> <code>%d</code>\n\n"+
			"<b>Commands:</b>\n"+
			"• <code>.approve</code> / <code>.disapprove</code> / <code>.blockpm</code> / <code>.unblockpm</code>\n"+
			"• <code>.pmpermit [on|off]</code>\n"+
			"• <code>.pmpermit list [approved|blocked|pending]</code>\n"+
			"• <code>.pmpermit test</code>",
			statusStr, pmpermit.DefaultMaxWarns, approved, pending, blocked)
		return ctx.EditOrReply(text)
	default:
		return ctx.EditOrReply("⚠️ Unknown option. Usage: <code>.pmpermit [on|off|status|test|list|unblock]</code>")
	}
}

