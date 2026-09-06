package pmpermit

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/pmpermit"
)

// Plugin provides PM security, warning counters, and access control.
type Plugin struct {
	svc *pmpermit.Service
}

// New creates a new PM permit plugin instance.
func New(svc *pmpermit.Service) *Plugin {
	return &Plugin{svc: svc}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "pmpermit"
}

// Description returns the plugin description.
func (p *Plugin) Description() string {
	return "Anti-spam shield and private message access control"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Service returns the underlying pmpermit service.
func (p *Plugin) Service() *pmpermit.Service {
	return p.svc
}

// Commands returns the registered commands.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "approve",
			Aliases:     []string{"allow"},
			Description: "Approve a user for private messaging",
			Usage:       ".approve (in PM, reply, or provide user_id)",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleApprove,
		},
		{
			Name:        "disapprove",
			Aliases:     []string{"disallow", "da"},
			Description: "Revoke PM approval for a user",
			Usage:       ".disapprove (in PM, reply, or provide user_id)",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleDisapprove,
		},
		{
			Name:        "blockpm",
			Aliases:     []string{"pmblock"},
			Description: "Block user from private messaging",
			Usage:       ".blockpm (in PM, reply, or provide user_id)",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleBlock,
		},
		{
			Name:        "pmpermit",
			Aliases:     []string{"pmguard"},
			Description: "Toggle or check PM permit shield status",
			Usage:       ".pmpermit [on|off]",
			Category:    "Security",
			Permission:  core.PermissionOwner,
			Handler:     p.handleToggle,
		},
	}
}

// HandleIncomingMessage intercepts incoming PMs to enforce PM permit warnings.
func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if p.svc == nil || !p.svc.IsEnabled() {
		return nil
	}
	if msg == nil || msg.Out {
		return nil
	}

	// Only process private chats (PeerUser)
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

	var peerInput tg.InputPeerClass = &tg.InputPeerUser{UserID: senderID}
	if u, ok := e.Users[senderID]; ok {
		peerInput = &tg.InputPeerUser{UserID: senderID, AccessHash: u.AccessHash}
	}

	_, err := p.svc.HandleIncomingPM(ctx, peerInput, senderID)
	return err
}

func (p *Plugin) resolveTargetUserID(ctx *core.Context) int64 {
	// 1. Argument
	if len(ctx.Args) > 0 {
		if id, err := strconv.ParseInt(ctx.Args[0], 10, 64); err == nil && id > 0 {
			return id
		}
	}

	// 2. Reply
	reply, err := ctx.GetReply()
	if err == nil && reply != nil && reply.SenderID != 0 {
		return reply.SenderID
	}

	// 3. Current PM peer
	if u, ok := ctx.PeerID.(*tg.InputPeerUser); ok && u != nil {
		return u.UserID
	}

	return 0
}

func (p *Plugin) handleApprove(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ PM Permit service is not configured.")
	}

	target := p.resolveTargetUserID(ctx)
	if target == 0 {
		return ctx.Reply("⚠️ Could not determine user. Reply to a message, run inside a PM, or provide user ID.")
	}

	reason := "Approved by owner"
	if len(ctx.Args) > 1 {
		reason = strings.Join(ctx.Args[1:], " ")
	}

	if err := p.svc.Approve(ctx.Ctx, target, reason, 0); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to approve user: %v", err))
	}

	return ctx.Reply(fmt.Sprintf("✅ <b>Approved</b> user <code>%d</code> for private messaging.", target))
}

func (p *Plugin) handleDisapprove(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ PM Permit service is not configured.")
	}

	target := p.resolveTargetUserID(ctx)
	if target == 0 {
		return ctx.Reply("⚠️ Could not determine user. Reply to a message, run inside a PM, or provide user ID.")
	}

	if err := p.svc.Disapprove(ctx.Ctx, target); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to revoke approval: %v", err))
	}

	return ctx.Reply(fmt.Sprintf("⚠️ <b>Revoked approval</b> for user <code>%d</code>.", target))
}

func (p *Plugin) handleBlock(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ PM Permit service is not configured.")
	}

	target := p.resolveTargetUserID(ctx)
	if target == 0 {
		return ctx.Reply("⚠️ Could not determine user. Reply to a message, run inside a PM, or provide user ID.")
	}

	reason := "Blocked by owner"
	if len(ctx.Args) > 1 {
		reason = strings.Join(ctx.Args[1:], " ")
	}

	if err := p.svc.Block(ctx.Ctx, target, reason); err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to block user: %v", err))
	}

	return ctx.Reply(fmt.Sprintf("⛔ <b>Blocked</b> user <code>%d</code> from private messaging.", target))
}

func (p *Plugin) handleToggle(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.Reply("⚠️ PM Permit service is not configured.")
	}

	if len(ctx.Args) > 0 {
		arg := strings.ToLower(ctx.Args[0])
		if arg == "on" || arg == "enable" || arg == "true" {
			p.svc.SetEnabled(true)
			return ctx.Reply("🛡️ <b>PM Permit</b> is now <b>ENABLED</b>.")
		} else if arg == "off" || arg == "disable" || arg == "false" {
			p.svc.SetEnabled(false)
			return ctx.Reply("⚠️ <b>PM Permit</b> is now <b>DISABLED</b>.")
		}
	}

	status := "ENABLED"
	if !p.svc.IsEnabled() {
		status = "DISABLED"
	}
	return ctx.Reply(fmt.Sprintf("🛡️ <b>PM Permit Status</b>: <b>%s</b>\nUsage: <code>.pmpermit [on|off]</code>", status))
}
