package info

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides user and chat informational inspection commands.
type Plugin struct{}

// New creates a new Info plugin.
func New() *Plugin {
	return &Plugin{}
}

// Name returns the unique plugin identifier.
func (p *Plugin) Name() string {
	return "info"
}

// Description returns a brief summary of the plugin.
func (p *Plugin) Description() string {
	return "User and chat information inspection utilities"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Shutdown cleans up resources.
func (p *Plugin) Shutdown() error {
	return nil
}

// Commands registers .whois and .chatinfo.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "whois",
			Aliases:     []string{"info", "userinfo"},
			Description: "Display detailed profile information of a user",
			Usage:       ".whois [username|id|reply]",
			Category:    "Info",
			Permission:  core.PermissionEveryone,
			Handler:     p.handleWhois,
		},
		{
			Name:        "chatinfo",
			Aliases:     []string{"groupinfo", "cinfo"},
			Description: "Display detailed metadata of the current chat/group/channel",
			Usage:       ".chatinfo",
			Category:    "Info",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleChatInfo,
		},
	}
}

// handleWhois resolves a user and displays their full profile.
func (p *Plugin) handleWhois(ctx *core.Context) error {
	inputUser, err := resolveInputUser(ctx)
	if err != nil {
		return ctx.Reply("⚠️ " + err.Error())
	}

	fullUser, err := ctx.GetFullUser(inputUser)
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to fetch user info: %v", err))
	}

	var u *tg.User
	for _, userClass := range fullUser.Users {
		if usr, ok := userClass.(*tg.User); ok {
			u = usr
			break
		}
	}

	if u == nil {
		return ctx.Reply("❌ Could not parse user details.")
	}

	var sb strings.Builder
	sb.WriteString("👤 <b>User Information</b>\n\n")
	sb.WriteString(fmt.Sprintf("• <b>ID</b>: <code>%d</code>\n", u.ID))
	sb.WriteString(fmt.Sprintf("• <b>First Name</b>: %s\n", escapeHTML(u.FirstName)))

	if u.LastName != "" {
		sb.WriteString(fmt.Sprintf("• <b>Last Name</b>: %s\n", escapeHTML(u.LastName)))
	}

	if u.Username != "" {
		sb.WriteString(fmt.Sprintf("• <b>Username</b>: @%s\n", u.Username))
	} else {
		sb.WriteString("• <b>Username</b>: <i>None</i>\n")
	}

	sb.WriteString(fmt.Sprintf("• <b>User Link</b>: <a href=\"tg://user?id=%d\">Permanent Link</a>\n", u.ID))

	// Status flags
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
		sb.WriteString(fmt.Sprintf("• <b>Bio</b>: <code>%s</code>\n", escapeHTML(fullUser.FullUser.About)))
	}

	return ctx.Reply(sb.String())
}

// handleChatInfo displays detailed metadata for groups, supergroups, and channels.
func (p *Plugin) handleChatInfo(ctx *core.Context) error {
	fullChat, err := ctx.GetFullChat()
	if err != nil {
		return ctx.Reply(fmt.Sprintf("❌ Failed to fetch chat info: %v", err))
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

	sb.WriteString(fmt.Sprintf("• <b>Title</b>: %s\n", escapeHTML(title)))
	sb.WriteString(fmt.Sprintf("• <b>ID</b>: <code>%d</code>\n", chatID))

	if ctx.Chat != nil && ctx.Chat.Type != "" {
		sb.WriteString(fmt.Sprintf("• <b>Type</b>: <code>%s</code>\n", ctx.Chat.Type))
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
			sb.WriteString(fmt.Sprintf("• <b>Description</b>: <i>%s</i>\n", escapeHTML(ch.About)))
		}
	case *tg.ChatFull:
		if ch.About != "" {
			sb.WriteString(fmt.Sprintf("• <b>Description</b>: <i>%s</i>\n", escapeHTML(ch.About)))
		}
		switch participants := ch.Participants.(type) {
		case *tg.ChatParticipants:
			sb.WriteString(fmt.Sprintf("• <b>Members</b>: %d\n", len(participants.Participants)))
		}
	}

	return ctx.Reply(sb.String())
}

// resolveInputUser determines the target user from reply, args, or self.
func resolveInputUser(ctx *core.Context) (tg.InputUserClass, error) {
	// 1. Reply to user
	reply, err := ctx.GetReply()
	if err == nil && reply != nil && reply.SenderID != 0 {
		return &tg.InputUser{UserID: reply.SenderID}, nil
	}

	// 2. Arg provided
	if len(ctx.Args) > 0 {
		arg := ctx.Args[0]
		// Numeric ID
		if uid, parseErr := strconv.ParseInt(arg, 10, 64); parseErr == nil && uid != 0 {
			return &tg.InputUser{UserID: uid}, nil
		}

		// Username resolution
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

	// 3. Self
	return &tg.InputUserSelf{}, nil
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
