package blacklist

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
)

var _ plugin.MessageHookPlugin = (*Plugin)(nil)

type compiledBlacklist struct {
	word string
	re   *regexp.Regexp
}

// Plugin manages chat word blacklists and automated message deletion.
type Plugin struct {
	db            database.Repository
	svcFunc       func() core.TelegramServicer
	cacheMu       sync.RWMutex
	chatBlacklist map[int64][]compiledBlacklist
	chatAccess    map[int64]time.Time
}

// New creates a new blacklist plugin instance.
func New(db database.Repository, svcFunc func() core.TelegramServicer) *Plugin {
	return &Plugin{
		db:            db,
		svcFunc:       svcFunc,
		chatBlacklist: make(map[int64][]compiledBlacklist),
		chatAccess:    make(map[int64]time.Time),
	}
}

func (p *Plugin) Name() string {
	return "blacklist"
}

func (p *Plugin) Init() error {
	return nil
}

// MessageHookPriority returns priority for the message hook (Security = 10).
func (p *Plugin) MessageHookPriority() int {
	return 10
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
		_ = ctx.EditOrReply("⚠️ Usage: <code>.blacklist &lt;word/phrase&gt;</code>")
		return errors.New("missing blacklist word")
	}

	word := strings.ToLower(strings.TrimSpace(ctx.RawArgs))
	if word == "" {
		_ = ctx.EditOrReply("⚠️ Blacklist word cannot be empty.")
		return errors.New("empty blacklist word")
	}

	chatID := p.getChatID(ctx)
	if err := p.db.AddBlacklist(ctx.Ctx, chatID, word); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to add to blacklist: %v", err))
		return err
	}

	p.cacheMu.Lock()
	delete(p.chatBlacklist, chatID)
	delete(p.chatAccess, chatID)
	p.cacheMu.Unlock()

	return ctx.EditOrReply(fmt.Sprintf("🚫 Added <code>%s</code> to chat blacklist.", html.EscapeString(word)))
}

func (p *Plugin) handleUnblacklist(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.unblacklist &lt;word/phrase&gt;</code>")
		return errors.New("missing blacklist word")
	}

	word := strings.ToLower(strings.TrimSpace(ctx.RawArgs))
	chatID := p.getChatID(ctx)

	if err := p.db.RemoveBlacklist(ctx.Ctx, chatID, word); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to remove from blacklist: %v", err))
		return err
	}

	p.cacheMu.Lock()
	delete(p.chatBlacklist, chatID)
	delete(p.chatAccess, chatID)
	p.cacheMu.Unlock()

	return ctx.EditOrReply(fmt.Sprintf("✅ Removed <code>%s</code> from chat blacklist.", html.EscapeString(word)))
}

func (p *Plugin) handleListBlacklists(ctx *core.Context) error {
	chatID := p.getChatID(ctx)
	words, err := p.db.ListBlacklists(ctx.Ctx, chatID)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to list blacklists: %v", err))
		return err
	}

	if len(words) == 0 {
		return ctx.EditOrReply("ℹ️ No blacklisted words in this chat.")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🚫 <b>Blacklisted Words in this chat (%d):</b>\n", len(words))
	for _, w := range words {
		fmt.Fprintf(&sb, "• <code>%s</code>\n", html.EscapeString(w))
	}

	return ctx.EditOrReply(sb.String())
}

// HandleIncomingMessage intercepts incoming non-command messages and deletes messages containing blacklisted words.
func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if isCommand || msg == nil || msg.Message == "" {
		return nil
	}
	if msg.Out {
		return nil
	}
	// Prevent bot loop: ignore messages sent by bot users
	if msg.FromID != nil {
		if uPeer, ok := msg.FromID.(*tg.PeerUser); ok {
			if senderUser, found := e.Users[uPeer.UserID]; found && senderUser != nil && senderUser.Bot {
				return nil
			}
		}
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

	p.cacheMu.RLock()
	items, ok := p.chatBlacklist[chatID]
	p.cacheMu.RUnlock()

	if !ok {
		var rawWords []string
		var err error
		rawWords, err = p.db.ListBlacklists(ctx, chatID)
		if err != nil {
			return nil
		}
		items = compileBlacklist(rawWords)
		p.cacheMu.Lock()
		if len(p.chatBlacklist) >= 500 {
			var oldestChat int64
			var oldestTime time.Time
			for c, t := range p.chatAccess {
				if oldestTime.IsZero() || t.Before(oldestTime) {
					oldestTime = t
					oldestChat = c
				}
			}
			if oldestChat != 0 {
				delete(p.chatBlacklist, oldestChat)
				delete(p.chatAccess, oldestChat)
			}
		}
		p.chatBlacklist[chatID] = items
		p.chatAccess[chatID] = time.Now()
		p.cacheMu.Unlock()
	} else {
		p.cacheMu.Lock()
		p.chatAccess[chatID] = time.Now()
		p.cacheMu.Unlock()
	}

	if len(items) == 0 {
		return nil
	}

	lowerText := strings.ToLower(msg.Message)
	for _, b := range items {
		matched := false
		if b.re != nil {
			matched = b.re.MatchString(lowerText)
		} else if b.word != "" {
			matched = strings.Contains(lowerText, b.word)
		}
		if matched {
			peer := extractPeerInput(msg.PeerID, e)
			if peer != nil {
				_ = svc.DeleteMessage(ctx, peer, []int{msg.ID})
			}
			break
		}
	}

	return nil
}

func compileBlacklist(raw []string) []compiledBlacklist {
	res := make([]compiledBlacklist, len(raw))
	for i, w := range raw {
		res[i] = compileBlacklistItem(w)
	}
	return res
}

func compileBlacklistItem(word string) compiledBlacklist {
	w := strings.ToLower(strings.TrimSpace(word))
	var re *regexp.Regexp
	if w != "" {
		pattern := `(?i)(?:^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(w) + `(?:$|[^\p{L}\p{N}_])`
		re, _ = regexp.Compile(pattern)
	}
	return compiledBlacklist{word: w, re: re}
}

func matchBlacklist(text, word string) bool {
	b := compileBlacklistItem(word)
	lowerText := strings.ToLower(text)
	if b.re != nil {
		return b.re.MatchString(lowerText)
	}
	return strings.Contains(lowerText, b.word)
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
	if peer == nil {
		return nil
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		if p.UserID == 0 {
			return nil
		}
		if u, ok := e.Users[p.UserID]; ok {
			return &tg.InputPeerUser{UserID: p.UserID, AccessHash: u.AccessHash}
		}
		return nil
	case *tg.PeerChat:
		if p.ChatID == 0 {
			return nil
		}
		return &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		if p.ChannelID == 0 {
			return nil
		}
		if ch, ok := e.Channels[p.ChannelID]; ok {
			return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: ch.AccessHash}
		}
		return nil
	}
	return nil
}
