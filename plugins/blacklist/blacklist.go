package blacklist

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// Plugin manages chat word blacklists and automated message deletion.
type Plugin struct {
	db      database.Repository
	svcFunc func() core.TelegramServicer
}

// New creates a new blacklist plugin instance.
func New(db database.Repository, svcFunc func() core.TelegramServicer) *Plugin {
	return &Plugin{
		db:      db,
		svcFunc: svcFunc,
	}
}

func (p *Plugin) Name() string {
	return "blacklist"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "blacklist",
			Description: "Add a word or phrase to the chat blacklist for auto-deletion",
			Usage:       ".blacklist <word/phrase>",
			Category:    "Moderation",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleBlacklist,
		},
		{
			Name:        "unblacklist",
			Aliases:     []string{"rmblacklist"},
			Description: "Remove a word or phrase from the chat blacklist",
			Usage:       ".unblacklist <word/phrase>",
			Category:    "Moderation",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleUnblacklist,
		},
		{
			Name:        "blacklists",
			Description: "List all blacklisted words in this chat",
			Category:    "Moderation",
			Permission:  core.PermissionSudo,
			GroupOnly:   true,
			Handler:     p.handleListBlacklists,
		},
	}
}

func (p *Plugin) getChatID(ctx *core.Context) int64 {
	if ctx.Chat != nil && ctx.Chat.ID != 0 {
		return ctx.Chat.ID
	}
	return ctx.SenderID()
}

func (p *Plugin) handleBlacklist(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.Reply("⚠️ Usage: <code>.blacklist &lt;word/phrase&gt;</code>")
		return errors.New("missing blacklist word")
	}

	word := strings.ToLower(strings.TrimSpace(ctx.RawArgs))
	if word == "" {
		_ = ctx.Reply("⚠️ Blacklist word cannot be empty.")
		return errors.New("empty blacklist word")
	}

	chatID := p.getChatID(ctx)
	if err := p.db.AddBlacklist(ctx.Ctx, chatID, word); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to add to blacklist: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🚫 Added <code>%s</code> to chat blacklist.", word))
}

func (p *Plugin) handleUnblacklist(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.Reply("⚠️ Usage: <code>.unblacklist &lt;word/phrase&gt;</code>")
		return errors.New("missing blacklist word")
	}

	word := strings.ToLower(strings.TrimSpace(ctx.RawArgs))
	chatID := p.getChatID(ctx)

	if err := p.db.RemoveBlacklist(ctx.Ctx, chatID, word); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to remove from blacklist: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("✅ Removed <code>%s</code> from chat blacklist.", word))
}

func (p *Plugin) handleListBlacklists(ctx *core.Context) error {
	chatID := p.getChatID(ctx)
	words, err := p.db.ListBlacklists(ctx.Ctx, chatID)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to list blacklists: %v", err))
		return err
	}

	if len(words) == 0 {
		return ctx.Reply("ℹ️ No blacklisted words in this chat.")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🚫 <b>Blacklisted Words in this chat (%d):</b>\n", len(words))
	for _, w := range words {
		fmt.Fprintf(&sb, "• <code>%s</code>\n", w)
	}

	return ctx.Reply(sb.String())
}

// HandleIncomingMessage intercepts incoming non-command messages and deletes messages containing blacklisted words.
func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if isCommand || msg == nil || msg.Message == "" {
		return nil
	}
	if msg.Out {
		return nil
	}
	if p.svcFunc == nil {
		return nil
	}
	svc := p.svcFunc()
	if svc == nil {
		return nil
	}

	chatID := extractChatID(msg.PeerID)
	if chatID == 0 {
		return nil
	}

	words, err := p.db.ListBlacklists(ctx, chatID)
	if err != nil || len(words) == 0 {
		return nil
	}

	text := msg.Message
	for _, word := range words {
		if matchBlacklist(text, word) {
			peer := extractPeerInput(msg.PeerID, e)
			if peer != nil {
				_ = svc.DeleteMessage(ctx, peer, []int{msg.ID})
			}
			break
		}
	}

	return nil
}

func extractChatID(peer tg.PeerClass) int64 {
	switch p := peer.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChat:
		return p.ChatID
	case *tg.PeerChannel:
		return p.ChannelID
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

func matchBlacklist(text, word string) bool {
	w := strings.ToLower(strings.TrimSpace(word))
	if w == "" {
		return false
	}
	lowerText := strings.ToLower(text)
	pattern := `(?i)(?:^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(w) + `(?:$|[^\p{L}\p{N}_])`
	re, err := regexp.Compile(pattern)
	if err == nil {
		return re.MatchString(lowerText)
	}
	return strings.Contains(lowerText, w)
}
