package afk

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// Plugin provides AFK (Away From Keyboard) status and auto-reply capabilities.
type Plugin struct {
	db       database.Repository
	ownerID  int64
	cooldown sync.Map // map[int64]time.Time (rate limit auto-replies per user/chat)
	svcFunc  func() core.TelegramServicer
}

// New creates a new AFK plugin instance.
func New(db database.Repository, ownerID int64, svcFunc func() core.TelegramServicer) *Plugin {
	return &Plugin{
		db:      db,
		ownerID: ownerID,
		svcFunc: svcFunc,
	}
}

func (p *Plugin) Name() string {
	return "afk"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "afk",
			Description: "Set AFK status with an optional reason",
			Usage:       ".afk [reason]",
			Category:    "AFK",
			Permission:  core.PermissionOwner,
			Handler:     p.handleAFKCommand,
		},
	}
}

func (p *Plugin) handleAFKCommand(ctx *core.Context) error {
	reason := "Away from keyboard"
	if len(ctx.Args) > 0 {
		reason = strings.TrimSpace(ctx.RawArgs)
	}

	ownerID := p.ownerID
	if ownerID == 0 {
		ownerID = ctx.SenderID()
	}

	if err := p.db.SetAFK(ctx.Ctx, ownerID, true, reason); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to set AFK: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🌙 <b>AFK Mode Activated!</b>\n<b>Reason:</b> <i>%s</i>", reason))
}

// HandleIncomingMessage intercepts all incoming messages to handle auto-reply and auto-unafk.
func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	ownerID := p.ownerID
	if ownerID == 0 {
		return nil
	}

	svc := p.svcFunc()
	if svc == nil {
		return nil
	}

	// 1. Message from Owner (outgoing) -> Auto-deactivate AFK
	if msg.Out {
		if isCommand && strings.EqualFold(cmdName, "afk") {
			return nil // Don't deactivate if running .afk
		}

		status, err := p.db.GetAFK(ctx, ownerID)
		if err != nil || status == nil || !status.IsAFK {
			return nil
		}

		// Turn off AFK
		_ = p.db.SetAFK(ctx, ownerID, false, "")
		duration := formatDuration(time.Since(status.Since))

		peer := extractPeerInput(msg.PeerID, e)
		if peer != nil {
			text := fmt.Sprintf("☀️ <b>Welcome back! AFK mode turned off.</b>\n<b>Away for:</b> <code>%s</code>", duration)
			_, _ = svc.SendMessage(ctx, peer, text)
		}
		return nil
	}

	// 2. Message from someone else -> Check if owner is AFK and if we should auto-reply
	status, err := p.db.GetAFK(ctx, ownerID)
	if err != nil || status == nil || !status.IsAFK {
		return nil
	}

	shouldReply := false
	senderID := extractSenderID(msg)

	// A. Private chat (DM)
	if _, isUser := msg.PeerID.(*tg.PeerUser); isUser {
		shouldReply = true
	} else {
		// B. Group: Check if replying to owner or mentioning owner
		if msg.ReplyTo != nil {
			if h, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok && h.ReplyToMsgID != 0 {
				peer := extractPeerInput(msg.PeerID, e)
				if peer != nil {
					repliedMsg, err := svc.GetMessage(ctx, peer, h.ReplyToMsgID)
					if err == nil && repliedMsg != nil {
						if repliedMsg.Out || extractSenderID(repliedMsg) == ownerID {
							shouldReply = true
						}
					}
				}
			}
		}

		// Check mention entities
		if !shouldReply && len(msg.Entities) > 0 {
			for _, ent := range msg.Entities {
				if m, ok := ent.(*tg.MessageEntityMentionName); ok && m.UserID == ownerID {
					shouldReply = true
					break
				}
			}
		}
	}

	if !shouldReply || senderID == 0 {
		return nil
	}

	// 3. Cooldown check: max 1 reply per 60 seconds per sender
	const afkCooldown = 60 * time.Second
	now := time.Now()
	if lastSent, loaded := p.cooldown.Load(senderID); loaded {
		if now.Sub(lastSent.(time.Time)) < afkCooldown {
			return nil
		}
	}
	p.cooldown.Store(senderID, now)

	peer := extractPeerInput(msg.PeerID, e)
	if peer == nil {
		return nil
	}

	sinceStr := formatDuration(time.Since(status.Since))
	replyText := fmt.Sprintf("🌙 <i>My owner is currently AFK!</i>\n<b>Reason:</b> %s\n<b>Since:</b> <code>%s ago</code>",
		status.Reason, sinceStr)

	_, _ = svc.SendMessage(ctx, peer, replyText)
	return nil
}

func extractSenderID(msg *tg.Message) int64 {
	if msg == nil {
		return 0
	}
	if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			return u.UserID
		}
	}
	return 0
}

func extractPeerInput(peer tg.PeerClass, e tg.Entities) tg.InputPeerClass {
	switch p := peer.(type) {
	case *tg.PeerUser:
		if u, ok := e.Users[p.UserID]; ok {
			return &tg.InputPeerUser{UserID: p.UserID, AccessHash: u.AccessHash}
		}
		return &tg.InputPeerSelf{}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		if ch, ok := e.Channels[p.ChannelID]; ok {
			return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: ch.AccessHash}
		}
	}
	return nil
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	mins := d / time.Minute
	d -= mins * time.Minute
	secs := d / time.Second

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	if secs > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", secs))
	}
	return strings.Join(parts, " ")
}
