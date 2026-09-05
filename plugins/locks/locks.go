package locks

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// Plugin provides chat permissions locking and unlocking.
type Plugin struct{}

// New creates a new locks Plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) Name() string {
	return "locks"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "lock",
			Description: "Lock a specific chat permission for non-admins",
			Usage:       ".lock <messages|media|stickers|gifs|links|polls|invites|pin|info|all>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleLock,
		},
		{
			Name:        "unlock",
			Description: "Unlock a specific chat permission for non-admins",
			Usage:       ".unlock <messages|media|stickers|gifs|links|polls|invites|pin|info|all>",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleUnlock,
		},
		{
			Name:        "locks",
			Description: "View active permission locks in this chat",
			Usage:       ".locks",
			Category:    "Admin",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleLocks,
		},
	}
}

func (p *Plugin) handleLock(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.Reply("⚠️ Usage: <code>.lock &lt;permission&gt;</code>\nValid options: <code>messages</code>, <code>media</code>, <code>stickers</code>, <code>gifs</code>, <code>links</code>, <code>polls</code>, <code>invites</code>, <code>pin</code>, <code>info</code>, <code>all</code>")
		return errors.New("missing lock permission argument")
	}

	perm := ctx.Args[0]
	current := getCurrentRights(ctx)
	updated, err := applyLock(current, perm, true)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ %v", err))
		return err
	}

	if err := ctx.EditChatDefaultBannedRights(updated); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to lock permission: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🔒 <b>Locked permission:</b> <code>%s</code> for this chat.", strings.ToLower(perm)))
}

func (p *Plugin) handleUnlock(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.Reply("⚠️ Usage: <code>.unlock &lt;permission&gt;</code>\nValid options: <code>messages</code>, <code>media</code>, <code>stickers</code>, <code>gifs</code>, <code>links</code>, <code>polls</code>, <code>invites</code>, <code>pin</code>, <code>info</code>, <code>all</code>")
		return errors.New("missing unlock permission argument")
	}

	perm := ctx.Args[0]
	current := getCurrentRights(ctx)
	updated, err := applyLock(current, perm, false)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ %v", err))
		return err
	}

	if err := ctx.EditChatDefaultBannedRights(updated); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to unlock permission: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🔓 <b>Unlocked permission:</b> <code>%s</code> for this chat.", strings.ToLower(perm)))
}

func (p *Plugin) handleLocks(ctx *core.Context) error {
	rights := getCurrentRights(ctx)
	return ctx.Reply(formatLocks(rights))
}

func applyLock(rights tg.ChatBannedRights, perm string, lock bool) (tg.ChatBannedRights, error) {
	perm = strings.ToLower(strings.TrimSpace(perm))
	switch perm {
	case "msg", "messages", "message":
		rights.SendMessages = lock
	case "media":
		rights.SendMedia = lock
	case "stickers", "sticker":
		rights.SendStickers = lock
	case "gifs", "gif":
		rights.SendGifs = lock
	case "games", "game":
		rights.SendGames = lock
	case "inline":
		rights.SendInline = lock
	case "links", "link":
		rights.EmbedLinks = lock
	case "polls", "poll":
		rights.SendPolls = lock
	case "invites", "invite":
		rights.InviteUsers = lock
	case "pin":
		rights.PinMessages = lock
	case "info":
		rights.ChangeInfo = lock
	case "all":
		rights.SendMessages = lock
		rights.SendMedia = lock
		rights.SendStickers = lock
		rights.SendGifs = lock
		rights.SendGames = lock
		rights.SendInline = lock
		rights.EmbedLinks = lock
		rights.SendPolls = lock
		rights.InviteUsers = lock
		rights.PinMessages = lock
		rights.ChangeInfo = lock
	default:
		return rights, fmt.Errorf("unknown lock type %q. Valid: messages, media, stickers, gifs, links, polls, invites, pin, info, all", perm)
	}
	return rights, nil
}

func getCurrentRights(ctx *core.Context) tg.ChatBannedRights {
	fullChat, err := ctx.GetFullChat()
	if err == nil && fullChat != nil {
		for _, ch := range fullChat.Chats {
			switch c := ch.(type) {
			case *tg.Channel:
				return c.DefaultBannedRights
			case *tg.Chat:
				return c.DefaultBannedRights
			}
		}
	}
	return tg.ChatBannedRights{}
}

func formatLocks(r tg.ChatBannedRights) string {
	icon := func(locked bool) string {
		if locked {
			return "🔒 <b>Locked</b>"
		}
		return "🔓 <i>Unlocked</i>"
	}

	return fmt.Sprintf(`🔐 <b>Chat Permissions & Locks:</b>

• <b>Messages:</b> %s
• <b>Media:</b> %s
• <b>Stickers:</b> %s
• <b>GIFs:</b> %s
• <b>Links:</b> %s
• <b>Polls:</b> %s
• <b>Invites:</b> %s
• <b>Pin:</b> %s
• <b>Change Info:</b> %s`,
		icon(r.SendMessages),
		icon(r.SendMedia),
		icon(r.SendStickers),
		icon(r.SendGifs),
		icon(r.EmbedLinks),
		icon(r.SendPolls),
		icon(r.InviteUsers),
		icon(r.PinMessages),
		icon(r.ChangeInfo),
	)
}
