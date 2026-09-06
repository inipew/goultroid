package userlog

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/userlog"
)

const asyncLogWorkers = 32

// Plugin provides tag/mention notification logging and event audit forwarding.
type Plugin struct {
	svc     *userlog.Service
	ownerID int64
	queue   chan func()
}

func New(svc *userlog.Service, ownerID int64) *Plugin {
	p := &Plugin{svc: svc, ownerID: ownerID, queue: make(chan func(), asyncLogWorkers)}
	for i := 0; i < asyncLogWorkers; i++ {
		go p.worker()
	}
	return p
}

func (p *Plugin) worker() {
	for job := range p.queue {
		func() {
			defer func() { _ = recover() }()
			job()
		}()
	}
}

func (p *Plugin) enqueue(job func()) {
	if job == nil { return }
	select {
	case p.queue <- job:
	default:
		// Logging is observational and must never become backpressure for commands.
	}
}

func (p *Plugin) Name() string { return "userlog" }
func (p *Plugin) Description() string { return "Forward tags, mentions, and new PMs to a dedicated log channel" }
func (p *Plugin) Init() error { return nil }

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{Name: "setlog", Aliases: []string{"setlogchat"}, Description: "Set the current chat/channel as the log destination", Usage: ".setlog", Category: "Admin", Permission: core.PermissionOwner, Handler: p.handleSetLog},
		{Name: "log", Aliases: []string{"logstatus"}, Description: "View or toggle log channel settings", Usage: ".log [tags|pms] [on|off]", Category: "Admin", Permission: core.PermissionOwner, Handler: p.handleLogStatus},
	}
}

// HandleIncomingMessage is deliberately non-blocking. UserLog is an observer;
// database reads and Telegram sends are isolated behind a bounded worker queue.
func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if p.svc == nil || msg == nil || msg.Out { return nil }

	senderID := int64(0)
	senderName := "Unknown User"
	if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			senderID = u.UserID
			if userObj, exists := e.Users[senderID]; exists {
				senderName = strings.TrimSpace(userObj.FirstName + " " + userObj.LastName)
				if senderName == "" { senderName = userObj.Username }
			}
		}
	}

	if peerUser, ok := msg.PeerID.(*tg.PeerUser); ok {
		if senderID == 0 { senderID = peerUser.UserID }
		name, id, text := senderName, senderID, msg.Message
		p.enqueue(func() { _ = p.svc.LogPM(context.Background(), name, id, text) })
		return nil
	}

	isMentioned := false
	if len(msg.Entities) > 0 {
		for _, ent := range msg.Entities {
			if m, ok := ent.(*tg.MessageEntityMentionName); ok && m.UserID == p.ownerID {
				isMentioned = true
				break
			}
		}
	}
	if !isMentioned { return nil }

	chatTitle := "Group Chat"
	switch c := msg.PeerID.(type) {
	case *tg.PeerChat:
		if chatObj, exists := e.Chats[c.ChatID]; exists && chatObj != nil { chatTitle = chatObj.Title }
	case *tg.PeerChannel:
		if chObj, exists := e.Channels[c.ChannelID]; exists { chatTitle = chObj.Title }
	}
	name, id, title, text := senderName, senderID, chatTitle, msg.Message
	p.enqueue(func() { _ = p.svc.LogMention(context.Background(), title, name, id, text) })
	return nil
}

func (p *Plugin) handleSetLog(ctx *core.Context) error {
	if p.svc == nil { return ctx.EditOrReply("⚠️ UserLog service is not configured.") }
	var chatID int64
	switch peer := ctx.PeerID.(type) {
	case *tg.InputPeerChat: chatID = peer.ChatID
	case *tg.InputPeerChannel: chatID = -peer.ChannelID
	default: return ctx.EditOrReply("⚠️ Please run <code>.setlog</code> inside a group or channel.")
	}
	if err := p.svc.SetLogChat(ctx.Ctx, chatID); err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to set log chat: %v", err)) }
	return ctx.EditOrReply(fmt.Sprintf("✅ <b>Log destination set!</b> All tags, mentions, and PMs will be forwarded here (ID: <code>%d</code>).", chatID))
}

func (p *Plugin) handleLogStatus(ctx *core.Context) error {
	if p.svc == nil { return ctx.EditOrReply("⚠️ UserLog service is not configured.") }
	if len(ctx.Args) >= 2 {
		category := strings.ToLower(ctx.Args[0])
		action := strings.ToLower(ctx.Args[1])
		enable := action == "on" || action == "enable" || action == "true"
		var settingKey string
		switch category {
		case "tags", "tag", "mentions": settingKey = userlog.SettingTagsEnable
		case "pms", "pm", "dms": settingKey = userlog.SettingPMsEnable
		default: return ctx.EditOrReply("⚠️ Unknown category. Choose <code>tags</code> or <code>pms</code>.")
		}
		if err := p.svc.SetFeatureEnabled(ctx.Ctx, settingKey, enable); err != nil { return ctx.EditOrReply(fmt.Sprintf("❌ Failed to update setting: %v", err)) }
		status := "DISABLED"; if enable { status = "ENABLED" }
		return ctx.EditOrReply(fmt.Sprintf("✅ Logging for <code>%s</code> is now <b>%s</b>.", category, status))
	}
	logChat, _ := p.svc.GetLogChat(ctx.Ctx)
	tagsOn, _ := p.svc.IsFeatureEnabled(ctx.Ctx, userlog.SettingTagsEnable)
	pmsOn, _ := p.svc.IsFeatureEnabled(ctx.Ctx, userlog.SettingPMsEnable)
	destStr := fmt.Sprintf("<code>%d</code>", logChat); if logChat == 0 { destStr = "<i>Not configured (use .setlog)</i>" }
	tagsStr := "❌ Disabled"; if tagsOn { tagsStr = "✅ Enabled" }
	pmsStr := "❌ Disabled"; if pmsOn { pmsStr = "✅ Enabled" }
	text := fmt.Sprintf("📋 <b>UserLog Configuration</b>\n\n• <b>Destination Chat:</b> %s\n• <b>Tag/Mention Logging:</b> %s\n• <b>PM Logging:</b> %s\n\nUsage: <code>.log [tags|pms] [on|off]</code>", destStr, tagsStr, pmsStr)
	return ctx.EditOrReply(text)
}
