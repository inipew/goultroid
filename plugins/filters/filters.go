package filters

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// Plugin manages automated chat keyword filters and auto-replies.
type Plugin struct {
	db      database.Repository
	svcFunc func() core.TelegramServicer
}

// New creates a new filters plugin instance.
func New(db database.Repository, svcFunc func() core.TelegramServicer) *Plugin {
	return &Plugin{
		db:      db,
		svcFunc: svcFunc,
	}
}

func (p *Plugin) Name() string {
	return "filters"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "filter",
			Description: "Save an automated keyword filter in this chat",
			Usage:       ".filter <keyword> <reply text> or reply to a message with .filter <keyword>",
			Category:    "Filters",
			Permission:  core.PermissionSudo,
			Handler:     p.handleFilter,
		},
		{
			Name:        "stop",
			Description: "Stop and delete a chat filter",
			Usage:       ".stop <keyword>",
			Category:    "Filters",
			Permission:  core.PermissionSudo,
			Handler:     p.handleStop,
		},
		{
			Name:        "filters",
			Description: "List all active filters in this chat",
			Category:    "Filters",
			Permission:  core.PermissionSudo,
			Handler:     p.handleList,
		},
	}
}

func (p *Plugin) getChatID(ctx *core.Context) int64 {
	if ctx.Chat != nil && ctx.Chat.ID != 0 {
		return ctx.Chat.ID
	}
	return ctx.SenderID()
}

func (p *Plugin) handleFilter(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.Reply("⚠️ Usage: <code>.filter &lt;keyword&gt; &lt;reply text&gt;</code> or reply to a message with <code>.filter &lt;keyword&gt;</code>")
		return errors.New("missing arguments")
	}

	keyword := strings.ToLower(ctx.Args[0])
	var replyText string

	if len(ctx.Args) >= 2 {
		replyText = strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0]))
	} else {
		reply, err := ctx.GetReply()
		if err != nil || reply == nil || reply.Text == "" {
			_ = ctx.Reply("⚠️ Please provide reply text or reply to a text message.")
			return errors.New("missing filter reply text")
		}
		replyText = reply.Text
	}

	chatID := p.getChatID(ctx)
	if err := p.db.SaveFilter(ctx.Ctx, chatID, keyword, replyText); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to save filter: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🎯 Filter <code>%s</code> saved successfully.", keyword))
}

func (p *Plugin) handleStop(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.Reply("⚠️ Usage: <code>.stop &lt;keyword&gt;</code>")
		return errors.New("missing filter keyword")
	}

	keyword := strings.ToLower(ctx.Args[0])
	chatID := p.getChatID(ctx)

	if err := p.db.DeleteFilter(ctx.Ctx, chatID, keyword); err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to stop filter: %v", err))
		return err
	}

	return ctx.Reply(fmt.Sprintf("🗑️ Filter <code>%s</code> stopped.", keyword))
}

func (p *Plugin) handleList(ctx *core.Context) error {
	chatID := p.getChatID(ctx)
	list, err := p.db.ListFilters(ctx.Ctx, chatID)
	if err != nil {
		_ = ctx.Reply(fmt.Sprintf("❌ Failed to list filters: %v", err))
		return err
	}

	if len(list) == 0 {
		return ctx.Reply("ℹ️ No active filters in this chat.")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🎯 <b>Active Filters in this chat (%d):</b>\n", len(list))
	for _, f := range list {
		fmt.Fprintf(&sb, "• <code>%s</code>\n", f.Keyword)
	}

	return ctx.Reply(sb.String())
}

// HandleIncomingMessage intercepts incoming non-command messages to evaluate chat keyword filters.
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

	filters, err := p.db.ListFilters(ctx, chatID)
	if err != nil || len(filters) == 0 {
		return nil
	}

	text := msg.Message
	for _, f := range filters {
		if matchFilter(text, f.Keyword) {
			peer := extractPeerInput(msg.PeerID, e)
			if peer != nil {
				_, _ = svc.SendMessage(ctx, peer, f.ReplyText)
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
		return &tg.InputPeerUser{UserID: p.UserID, AccessHash: 0}
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
		return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: 0}
	}
	return nil
}

var (
	filterRegexMu    sync.RWMutex
	filterRegexCache = make(map[string]*regexp.Regexp)
)

func matchFilter(text, keyword string) bool {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return false
	}
	lowerText := strings.ToLower(text)

	filterRegexMu.RLock()
	re, ok := filterRegexCache[kw]
	filterRegexMu.RUnlock()

	if !ok {
		pattern := `(?i)(?:^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(kw) + `(?:$|[^\p{L}\p{N}_])`
		compiled, err := regexp.Compile(pattern)
		if err == nil {
			filterRegexMu.Lock()
			filterRegexCache[kw] = compiled
			filterRegexMu.Unlock()
			re = compiled
		}
	}

	if re != nil {
		return re.MatchString(lowerText)
	}
	return strings.Contains(lowerText, kw)
}
