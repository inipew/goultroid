package info

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

var (
	_ execution.CapabilityProvider = (*Plugin)(nil)
)

type Plugin struct{}

func New() *Plugin                    { return &Plugin{} }
func (p *Plugin) Name() string        { return "info" }
func (p *Plugin) Description() string { return "User and chat information inspection utilities" }
func (p *Plugin) Init() error         { return nil }
func (p *Plugin) Shutdown() error     { return nil }

// Capabilities declares the capabilities provided by this plugin (§4, §28 bug16_1).
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "info",
			Name:        "Info",
			Description: "User and chat information inspection utilities",
			Category:    "Info",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

func (p *Plugin) Commands() []core.Command {
	infoSurfaces := execution.SurfaceUserbot | execution.SurfaceAssistant
	return []core.Command{
		{Name: "whois", Aliases: []string{"info", "userinfo"}, Description: "Display detailed profile information of a user", Usage: ".whois [username|id|reply]", Category: "Info", Permission: core.PermissionEveryone, Surfaces: infoSurfaces, Handler: p.handleWhois},
		{Name: "chatinfo", Aliases: []string{"groupinfo", "cinfo"}, Description: "Display detailed metadata of the current chat/group/channel", Usage: ".chatinfo", Category: "Info", Permission: core.PermissionSudo, GroupOnly: true, Surfaces: infoSurfaces, Handler: p.handleChatInfo},
		{Name: "id", Aliases: []string{"chatid"}, Description: "Display current chat ID, chat type, and sender ID", Usage: ".id", Category: "Info", Permission: core.PermissionEveryone, Surfaces: infoSurfaces, Handler: p.handleID},
	}
}

func (p *Plugin) handleWhois(ctx *core.Context) error {
	inputUser, err := resolveInputUser(ctx)
	if err != nil {
		return ctx.EditOrReply("⚠️ " + err.Error())
	}
	fullUser, err := ctx.GetFullUser(inputUser)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to fetch user info: %v", err))
	}
	var u *tg.User
	for _, userClass := range fullUser.Users {
		if usr, ok := userClass.(*tg.User); ok {
			u = usr
			break
		}
	}
	if u == nil {
		return ctx.EditOrReply("❌ Could not parse user details.")
	}

	var sb strings.Builder
	sb.WriteString("👤 <b>User Information</b>\n\n")
	sb.WriteString(fmt.Sprintf("• <b>ID</b>: <code>%d</code>\n", u.ID))
	sb.WriteString(fmt.Sprintf("• <b>First Name</b>: %s\n", core.EscapeHTML(u.FirstName)))
	if u.LastName != "" {
		sb.WriteString(fmt.Sprintf("• <b>Last Name</b>: %s\n", core.EscapeHTML(u.LastName)))
	}
	if u.Username != "" {
		sb.WriteString(fmt.Sprintf("• <b>Username</b>: @%s\n", core.EscapeHTML(u.Username)))
	} else {
		sb.WriteString("• <b>Username</b>: <i>None</i>\n")
	}
	sb.WriteString(fmt.Sprintf("• <b>User Link</b>: <a href=\"tg://user?id=%d\">Permanent Link</a>\n", u.ID))
	if u.Bot {
		sb.WriteString("• <b>Is Bot</b>: Yes 🤖\n")
	} else {
		sb.WriteString("• <b>Is Bot</b>: No\n")
	}
	if u.Premium {
		sb.WriteString("• <b>Premium</b>: Yes ⭐️\n")
	}
	if u.Verified {
		sb.WriteString("• <b>Verified</b>: Yes ✅\n")
	}
	if u.Scam {
		sb.WriteString("• <b>Scam</b>: ⚠️ Yes\n")
	}
	if u.Fake {
		sb.WriteString("• <b>Fake</b>: ⚠️ Yes\n")
	}
	if u.Restricted {
		sb.WriteString("• <b>Restricted</b>: Yes ⛔\n")
	}
	if fullUser.FullUser.CommonChatsCount > 0 {
		sb.WriteString(fmt.Sprintf("• <b>Common Chats</b>: %d\n", fullUser.FullUser.CommonChatsCount))
	}
	if fullUser.FullUser.About != "" {
		sb.WriteString(fmt.Sprintf("• <b>Bio</b>: <code>%s</code>\n", core.EscapeHTML(fullUser.FullUser.About)))
	}
	return ctx.EditOrReply(sb.String())
}

func (p *Plugin) handleChatInfo(ctx *core.Context) error {
	fullChat, err := ctx.GetFullChat()
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to fetch chat info: %v", err))
	}
	var sb strings.Builder
	sb.WriteString("👥 <b>Chat Information</b>\n\n")
	title := "Unknown"
	if ctx.Chat != nil && ctx.Chat.Title != "" {
		title = ctx.Chat.Title
	}
	var chatID int64
	if ctx.Chat != nil {
		chatID = ctx.Chat.ID
	}
	sb.WriteString(fmt.Sprintf("• <b>Title</b>: %s\n", core.EscapeHTML(title)))
	sb.WriteString(fmt.Sprintf("• <b>ID</b>: <code>%d</code>\n", chatID))
	if ctx.Chat != nil && ctx.Chat.Type != "" {
		sb.WriteString(fmt.Sprintf("• <b>Type</b>: <code>%s</code>\n", core.EscapeHTML(ctx.Chat.Type)))
	}
	switch ch := fullChat.FullChat.(type) {
	case *tg.ChannelFull:
		if ch.ParticipantsCount > 0 {
			sb.WriteString(fmt.Sprintf("• <b>Members</b>: %d\n", ch.ParticipantsCount))
		}
		if ch.AdminsCount > 0 {
			sb.WriteString(fmt.Sprintf("• <b>Admins</b>: %d\n", ch.AdminsCount))
		}
		if ch.BannedCount > 0 {
			sb.WriteString(fmt.Sprintf("• <b>Banned</b>: %d\n", ch.BannedCount))
		}
		if ch.KickedCount > 0 {
			sb.WriteString(fmt.Sprintf("• <b>Kicked</b>: %d\n", ch.KickedCount))
		}
		if ch.SlowmodeSeconds > 0 {
			sb.WriteString(fmt.Sprintf("• <b>Slowmode</b>: %ds\n", ch.SlowmodeSeconds))
		}
		if ch.About != "" {
			sb.WriteString(fmt.Sprintf("• <b>Description</b>: <i>%s</i>\n", core.EscapeHTML(ch.About)))
		}
	case *tg.ChatFull:
		if ch.About != "" {
			sb.WriteString(fmt.Sprintf("• <b>Description</b>: <i>%s</i>\n", core.EscapeHTML(ch.About)))
		}
		if participants, ok := ch.Participants.(*tg.ChatParticipants); ok {
			sb.WriteString(fmt.Sprintf("• <b>Members</b>: %d\n", len(participants.Participants)))
		}
	}
	return ctx.EditOrReply(sb.String())
}

// resolveInputUser deliberately checks explicit arguments before fetching a reply.
// This avoids a Telegram GetReply RPC for the common `.whois 123456` form.
func resolveInputUser(ctx *core.Context) (tg.InputUserClass, error) {
	if len(ctx.Args) > 0 {
		arg := strings.TrimSpace(ctx.Args[0])
		if uid, parseErr := strconv.ParseInt(arg, 10, 64); parseErr == nil && uid > 0 {
			return &tg.InputUser{UserID: uid}, nil
		}
		resolved, resErr := ctx.ResolveUsername(arg)
		if resErr != nil {
			return nil, fmt.Errorf("failed to resolve user %q: %w", arg, resErr)
		}
		for _, u := range resolved.Users {
			if usr, ok := u.(*tg.User); ok {
				return &tg.InputUser{UserID: usr.ID, AccessHash: usr.AccessHash}, nil
			}
		}
		return nil, fmt.Errorf("username %q is not a user", arg)
	}
	if reply, err := ctx.GetReply(); err == nil && reply != nil && reply.SenderID != 0 {
		return &tg.InputUser{UserID: reply.SenderID}, nil
	}
	return &tg.InputUserSelf{}, nil
}

func (p *Plugin) handleID(ctx *core.Context) error {
	var sb strings.Builder
	sb.WriteString("🆔 <b>Chat &amp; User Information</b>\n\n")
	if ctx.Chat != nil {
		sb.WriteString(fmt.Sprintf("• <b>Chat ID:</b> <code>%d</code>\n", ctx.Chat.ID))
		sb.WriteString(fmt.Sprintf("• <b>Chat Type:</b> <code>%s</code>\n", core.EscapeHTML(ctx.Chat.Type)))
		if ctx.Chat.Title != "" {
			sb.WriteString(fmt.Sprintf("• <b>Chat Title:</b> %s\n", core.EscapeHTML(ctx.Chat.Title)))
		}
	}
	if ctx.Sender != nil {
		sb.WriteString(fmt.Sprintf("• <b>Sender ID:</b> <code>%d</code>\n", ctx.Sender.ID))
	}
	if ctx.Message != nil && ctx.Message.ReplyToID != 0 {
		sb.WriteString(fmt.Sprintf("• <b>Reply Msg ID:</b> <code>%d</code>\n", ctx.Message.ReplyToID))
		if reply, err := ctx.GetReply(); err == nil && reply != nil && reply.SenderID != 0 {
			sb.WriteString(fmt.Sprintf("• <b>Reply Sender ID:</b> <code>%d</code>\n", reply.SenderID))
		}
	}
	return ctx.EditOrReply(sb.String())
}
