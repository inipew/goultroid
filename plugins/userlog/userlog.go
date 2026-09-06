package userlog

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/userlog"
)

// Plugin provides tag/mention notification logging and event audit forwarding.
type Plugin struct {
	svc     *userlog.Service
	ownerID int64
}

// New creates a new userlog plugin instance.
func New(svc *userlog.Service, ownerID int64) *Plugin {
	return &Plugin{
		svc:     svc,
		ownerID: ownerID,
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "userlog"
}

// Description returns the plugin description.
func (p *Plugin) Description() string {
	return "Forward tags, mentions, and new PMs to a dedicated log channel"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Commands returns registered userlog commands.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "setlog",
			Aliases:     []string{"setlogchat"},
			Description: "Set the current chat/channel as the log destination",
			Usage:       ".setlog",
			Category:    "Admin",
			Permission:  core.PermissionOwner,
			Handler:     p.handleSetLog,
		},
		{
			Name:        "log",
			Aliases:     []string{"logstatus"},
			Description: "View or toggle log channel settings",
			Usage:       ".log [tags|pms] [on|off]",
			Category:    "Admin",
			Permission:  core.PermissionOwner,
			Handler:     p.handleLogStatus,
		},
	}
}

// HandleIncomingMessage intercepts incoming mentions and PMs to log them in the audit channel.
func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if p.svc == nil || msg == nil || msg.Out {
		return nil
	}

	senderID := int64(0)
	senderName := "Unknown User"
	if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			senderID = u.UserID
			if userObj, exists := e.Users[senderID]; exists {
				senderName = strings.TrimSpace(userObj.FirstName + " " + userObj.LastName)
				if senderName == "" {
					senderName = userObj.Username
				}
			}
		}
	}

	// 1. Check if this is a Private Message
	if peerUser, ok := msg.PeerID.(*tg.PeerUser); ok {
		if senderID == 0 {
			senderID = peerUser.UserID
		}
		return p.svc.LogPM(ctx, senderName, senderID, msg.Message)
	}

	// 2. Check if this is a Group / Channel Mention
	isMentioned := false
	if len(msg.Entities) > 0 {
		for _, ent := range msg.Entities {
			if m, ok := ent.(*tg.MessageEntityMentionName); ok && m.UserID == p.ownerID {
				isMentioned = true
				break
			}
		}
	}

	if isMentioned {
		chatTitle := "Group Chat"
		switch c := msg.PeerID.(type) {
		case *tg.PeerChat:
			if chatObj, exists := e.Chats[c.ChatID]; exists && chatObj != nil {
				chatTitle = chatObj.Title
			}
		case *tg.PeerChannel:
			if chObj, exists := e.Channels[c.ChannelID]; exists {
				chatTitle = chObj.Title
			}
		}

		return p.svc.LogMention(ctx, chatTitle, senderName, senderID, msg.Message)
	}

	return nil
}

func (p *Plugin) handleSetLog(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ UserLog service is not configured.")
	}

	var chatID int64
	switch peer := ctx.PeerID.(type) {
	case *tg.InputPeerChat:
		chatID = peer.ChatID
	case *tg.InputPeerChannel:
		// Channels / supergroups use negative ID notation for unified bots
		chatID = -peer.ChannelID
	default:
		return ctx.EditOrReply("⚠️ Please run <code>.setlog</code> inside a group or channel.")
	}

	if err := p.svc.SetLogChat(ctx.Ctx, chatID); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to set log chat: %v", err))
	}

	return ctx.EditOrReply(fmt.Sprintf("✅ <b>Log destination set!</b> All tags, mentions, and PMs will be forwarded here (ID: <code>%d</code>).", chatID))
}

func (p *Plugin) handleLogStatus(ctx *core.Context) error {
	if p.svc == nil {
		return ctx.EditOrReply("⚠️ UserLog service is not configured.")
	}

	if len(ctx.Args) >= 2 {
		category := strings.ToLower(ctx.Args[0])
		action := strings.ToLower(ctx.Args[1])
		enable := action == "on" || action == "enable" || action == "true"

		var settingKey string
		switch category {
		case "tags", "tag", "mentions":
			settingKey = userlog.SettingTagsEnable
		case "pms", "pm", "dms":
			settingKey = userlog.SettingPMsEnable
		default:
			return ctx.EditOrReply("⚠️ Unknown category. Choose <code>tags</code> or <code>pms</code>.")
		}

		if err := p.svc.SetFeatureEnabled(ctx.Ctx, settingKey, enable); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Failed to update setting: %v", err))
		}

		status := "DISABLED"
		if enable {
			status = "ENABLED"
		}
		return ctx.EditOrReply(fmt.Sprintf("✅ Logging for <code>%s</code> is now <b>%s</b>.", category, status))
	}

	logChat, _ := p.svc.GetLogChat(ctx.Ctx)
	tagsOn, _ := p.svc.IsFeatureEnabled(ctx.Ctx, userlog.SettingTagsEnable)
	pmsOn, _ := p.svc.IsFeatureEnabled(ctx.Ctx, userlog.SettingPMsEnable)

	destStr := fmt.Sprintf("<code>%d</code>", logChat)
	if logChat == 0 {
		destStr = "<i>Not configured (use .setlog)</i>"
	}

	tagsStr := "❌ Disabled"
	if tagsOn {
		tagsStr = "✅ Enabled"
	}
	pmsStr := "❌ Disabled"
	if pmsOn {
		pmsStr = "✅ Enabled"
	}

	text := fmt.Sprintf(
		"📋 <b>UserLog Configuration</b>\n\n"+
			"• <b>Destination Chat:</b> %s\n"+
			"• <b>Tag/Mention Logging:</b> %s\n"+
			"• <b>PM Logging:</b> %s\n\n"+
			"Usage: <code>.log [tags|pms] [on|off]</code>",
		destStr, tagsStr, pmsStr,
	)

	return ctx.EditOrReply(text)
}
