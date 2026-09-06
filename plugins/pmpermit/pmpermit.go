package pmpermit

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/pmpermit"
)

type Plugin struct { svc *pmpermit.Service }
func New(svc *pmpermit.Service) *Plugin { return &Plugin{svc: svc} }
func (p *Plugin) Name() string { return "pmpermit" }
func (p *Plugin) Description() string { return "Anti-spam shield and private message access control" }
func (p *Plugin) Init() error { return nil }
func (p *Plugin) Service() *pmpermit.Service { return p.svc }

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{Name: "approve", Aliases: []string{"allow"}, Description: "Approve a user for private messaging", Usage: ".approve (in PM, reply, or provide user_id)", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleApprove},
		{Name: "disapprove", Aliases: []string{"disallow", "da"}, Description: "Revoke PM approval for a user", Usage: ".disapprove (in PM, reply, or provide user_id)", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleDisapprove},
		{Name: "blockpm", Aliases: []string{"pmblock"}, Description: "Block user from private messaging", Usage: ".blockpm (in PM, reply, or provide user_id)", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleBlock},
		{Name: "pmpermit", Aliases: []string{"pmguard"}, Description: "Toggle or check PM permit shield status", Usage: ".pmpermit [on|off]", Category: "Security", Permission: core.PermissionOwner, Handler: p.handleToggle},
	}
}

func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if p.svc == nil || !p.svc.IsEnabled() || msg == nil { return nil }
	if msg.Out {
		// 1. NEVER auto-approve if this is a command (e.g. .disapprove, .blockpm, .a, etc.)
		if isCommand || cmdName != "" || strings.HasPrefix(msg.Message, ".") {
			return nil
		}
		// 2. NEVER auto-approve if this is a bot-generated warning or notification message
		if p.svc.IsPMPermitMessage(msg.Message) {
			return nil
		}
		// Auto-allow when owner legitimately initiates PM to an unapproved user
		if peerUser, ok := msg.PeerID.(*tg.PeerUser); ok && peerUser != nil {
			targetID := peerUser.UserID
			// Ignore if this message ID matches a recorded warning message
			if p.svc.IsWarnID(targetID, msg.ID) {
				return nil
			}
			// Avoid self-approve
			if targetID != 0 && targetID != p.svc.OwnerID() && !p.svc.IsSudoID(targetID) {
				// Don't auto-allow if user is explicitly blocked
				if p.svc.IsBlocked(ctx, targetID) {
					return nil
				}
				peer := tg.InputPeerClass(&tg.InputPeerUser{UserID: targetID})
				if u, ok := e.Users[targetID]; ok {
					peer = &tg.InputPeerUser{UserID: targetID, AccessHash: u.AccessHash}
				}
				_ = p.svc.AutoApproveOutgoing(ctx, peer, targetID)
			}
		}
		return nil
	}
	peerUser, ok := msg.PeerID.(*tg.PeerUser); if !ok || peerUser == nil { return nil }
	senderID := peerUser.UserID
	if msg.FromID != nil { if u, ok := msg.FromID.(*tg.PeerUser); ok { senderID = u.UserID } }
	peerInput := tg.InputPeerClass(&tg.InputPeerUser{UserID: senderID})
	if u, ok := e.Users[senderID]; ok { peerInput = &tg.InputPeerUser{UserID: senderID, AccessHash: u.AccessHash} }
	handled, err := p.svc.HandleIncomingPM(ctx, peerInput, senderID)
	if err != nil { return err }
	if handled && isCommand {
		core.MarkMessageHandled(senderID, msg.ID)
	}
	return nil
}

func (p *Plugin) resolveTargetUserID(ctx *core.Context) int64 {
	if len(ctx.Args) > 0 { if id, err := strconv.ParseInt(ctx.Args[0], 10, 64); err == nil && id > 0 { return id } }
	if reply, err := ctx.GetReply(); err == nil && reply != nil && reply.SenderID != 0 { return reply.SenderID }
	if u, ok := ctx.PeerID.(*tg.InputPeerUser); ok && u != nil { return u.UserID }
	return 0
}
func (p *Plugin) handleApprove(ctx *core.Context) error {
	if p.svc == nil { return ctx.EditOrReply("⚠️ PM Permit service is not configured.") }
	target := p.resolveTargetUserID(ctx); if target == 0 { return ctx.EditOrReply("⚠️ Could not determine user. Reply to a message, run inside a PM, or provide user ID.") }
	reason := "Approved by owner"; if len(ctx.Args) > 1 { reason = strings.Join(ctx.Args[1:], " ") }
	if err := p.svc.Approve(ctx.Ctx, target, reason, 0); err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to approve user: %v", err)) }
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("✅ <b>Approved</b> user <code>%d</code> for private messaging.", target), 4*time.Second)
}
func (p *Plugin) handleDisapprove(ctx *core.Context) error {
	if p.svc == nil { return ctx.EditOrReply("⚠️ PM Permit service is not configured.") }
	target := p.resolveTargetUserID(ctx); if target == 0 { return ctx.EditOrReply("⚠️ Could not determine user. Reply to a message, run inside a PM, or provide user ID.") }
	if err := p.svc.Disapprove(ctx.Ctx, target); err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to revoke approval: %v", err)) }
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("⚠️ <b>Revoked approval</b> for user <code>%d</code>.", target), 4*time.Second)
}
func (p *Plugin) handleBlock(ctx *core.Context) error {
	if p.svc == nil { return ctx.EditOrReply("⚠️ PM Permit service is not configured.") }
	target := p.resolveTargetUserID(ctx); if target == 0 { return ctx.EditOrReply("⚠️ Could not determine user. Reply to a message, run inside a PM, or provide user ID.") }
	reason := "Blocked by owner"; if len(ctx.Args) > 1 { reason = strings.Join(ctx.Args[1:], " ") }
	// Try to resolve peer with AccessHash for real Telegram block
	var peer tg.InputPeerClass = &tg.InputPeerUser{UserID: target}
	if p, _, err := ctx.Peer().ResolveTargetUser(); err == nil && p != nil {
		peer = p
	} else if u, ok := ctx.PeerID.(*tg.InputPeerUser); ok && u.UserID == target {
		peer = u
	}
	// Prefer BlockWithPeer for real RPC, fallback to DB-only Block
	var err error
	if peer != nil {
		err = p.svc.BlockWithPeer(ctx.Ctx, peer, target, reason)
	} else {
		err = p.svc.Block(ctx.Ctx, target, reason)
	}
	if err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to block user: %v", err)) }
	return ctx.EditOrReplyWithDelay(fmt.Sprintf("⛔ <b>Blocked</b> user <code>%d</code> from private messaging.", target), 4*time.Second)
}
func (p *Plugin) handleToggle(ctx *core.Context) error {
	if p.svc == nil { return ctx.EditOrReply("⚠️ PM Permit service is not configured.") }
	if len(ctx.Args) > 0 { arg := strings.ToLower(ctx.Args[0]); if arg == "on" || arg == "enable" || arg == "true" { p.svc.SetEnabled(true); return ctx.EditOrReply("🛡️ <b>PM Permit</b> is now <b>ENABLED</b>.") }; if arg == "off" || arg == "disable" || arg == "false" { p.svc.SetEnabled(false); return ctx.EditOrReply("⚠️ <b>PM Permit</b> is now <b>DISABLED</b>.") } }
	status := "ENABLED"; if !p.svc.IsEnabled() { status = "DISABLED" }
	return ctx.EditOrReply(fmt.Sprintf("🛡️ <b>PM Permit Status</b>: <b>%s</b>\nUsage: <code>.pmpermit [on|off]</code>", status))
}
