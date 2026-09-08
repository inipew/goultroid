package filters

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
)

const (
	filterCacheTTL = 10 * time.Minute
	filterCooldown = 5 * time.Second
)

var _ plugin.MessageHookPlugin = (*Plugin)(nil)

type compiledFilter struct {
	keyword   string
	replyText string
	re        *regexp.Regexp
}

type Plugin struct {
	db          Repository
	svcFunc     func() core.TelegramServicer
	cacheMu     sync.RWMutex
	chatFilters map[int64][]compiledFilter
	chatAccess  map[int64]time.Time
	cooldownMu  sync.Mutex
	lastReply   map[string]time.Time
}

func New(db Repository, svcFunc func() core.TelegramServicer) *Plugin {
	return &Plugin{db: db, svcFunc: svcFunc, chatFilters: make(map[int64][]compiledFilter), chatAccess: make(map[int64]time.Time), lastReply: make(map[string]time.Time)}
}

func (p *Plugin) Name() string             { return "filters" }
func (p *Plugin) Init() error              { return nil }
func (p *Plugin) MessageHookPriority() int { return 20 }

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{Name: "filter", Description: "Save an automated keyword filter in this chat", Usage: ".filter <keyword> <reply text> or reply to a message with .filter <keyword>", Category: "Filters", Permission: core.PermissionSudo, GroupOnly: true, Handler: p.handleFilter},
		{Name: "stop", Description: "Stop and delete a chat filter", Usage: ".stop <keyword>", Category: "Filters", Permission: core.PermissionSudo, GroupOnly: true, Handler: p.handleStop},
		{Name: "filters", Description: "List all active filters in this chat", Category: "Filters", Permission: core.PermissionSudo, GroupOnly: true, Handler: p.handleList},
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
		_ = ctx.EditOrReply("⚠️ Usage: <code>.filter &lt;keyword&gt; &lt;reply text&gt;</code> or reply to a message with <code>.filter &lt;keyword&gt;</code>")
		return errors.New("missing arguments")
	}
	if p.db == nil {
		_ = ctx.EditOrReply("❌ Filters database is unavailable.")
		return errors.New("filters: database is unavailable")
	}
	keyword := strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	if keyword == "" {
		_ = ctx.EditOrReply("⚠️ Filter keyword cannot be empty.")
		return errors.New("empty filter keyword")
	}
	var replyText string
	if len(ctx.Args) >= 2 {
		replyText = strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0]))
	} else {
		reply, err := ctx.GetReply()
		if err != nil || reply == nil || reply.Text == "" {
			_ = ctx.EditOrReply("⚠️ Please provide reply text or reply to a text message.")
			return errors.New("missing filter reply text")
		}
		replyText = reply.Text
	}
	if replyText == "" {
		_ = ctx.EditOrReply("⚠️ Filter reply cannot be empty.")
		return errors.New("empty filter reply")
	}
	chatID := p.getChatID(ctx)
	if err := p.db.SaveFilter(ctx.Ctx, chatID, keyword, replyText); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to save filter: %v", err))
		return err
	}
	p.invalidateChat(chatID)
	return ctx.EditOrReply(fmt.Sprintf("🎯 Filter <code>%s</code> saved successfully.", keyword))
}

func (p *Plugin) handleStop(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.stop &lt;keyword&gt;</code>")
		return errors.New("missing filter keyword")
	}
	if p.db == nil {
		_ = ctx.EditOrReply("❌ Filters database is unavailable.")
		return errors.New("filters: database is unavailable")
	}
	keyword := strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	chatID := p.getChatID(ctx)
	if err := p.db.DeleteFilter(ctx.Ctx, chatID, keyword); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to stop filter: %v", err))
		return err
	}
	p.invalidateChat(chatID)
	return ctx.EditOrReply(fmt.Sprintf("🗑️ Filter <code>%s</code> stopped.", keyword))
}

func (p *Plugin) handleList(ctx *core.Context) error {
	if p.db == nil {
		_ = ctx.EditOrReply("❌ Filters database is unavailable.")
		return errors.New("filters: database is unavailable")
	}
	chatID := p.getChatID(ctx)
	list, err := p.db.ListFilters(ctx.Ctx, chatID)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to list filters: %v", err))
		return err
	}
	if len(list) == 0 {
		return ctx.EditOrReply("ℹ️ No active filters in this chat.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🎯 <b>Active Filters in this chat (%d):</b>\n", len(list))
	for _, f := range list {
		fmt.Fprintf(&sb, "• <code>%s</code>\n", f.Keyword)
	}
	return ctx.EditOrReply(sb.String())
}

func (p *Plugin) invalidateChat(chatID int64) {
	p.cacheMu.Lock()
	delete(p.chatFilters, chatID)
	delete(p.chatAccess, chatID)
	p.cacheMu.Unlock()
}

func (p *Plugin) HandleIncomingMessage(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
	if isCommand || msg == nil || msg.Message == "" || msg.Out {
		return nil
	}
	if decision := core.GetMessageDecision(ctx); decision != nil && (decision.IsSuppressedFilters() || decision.IsSuppressedAutomation()) {
		return nil
	}
	if msg.FromID != nil {
		if uPeer, ok := msg.FromID.(*tg.PeerUser); ok {
			if senderUser, found := e.Users[uPeer.UserID]; found && senderUser != nil && senderUser.Bot {
				return nil
			}
		}
	}
	if p.svcFunc == nil || p.db == nil {
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

	filters, ok := p.getCachedFilters(chatID)
	if !ok {
		rawFilters, err := p.db.ListFilters(ctx, chatID)
		if err != nil {
			return nil
		}
		filters = compileFilters(rawFilters)
		p.cacheFilters(chatID, filters)
	}
	if len(filters) == 0 {
		return nil
	}

	lowerText := strings.ToLower(msg.Message)
	for _, f := range filters {
		matched := (f.re != nil && f.re.MatchString(lowerText)) || (f.re == nil && f.keyword != "" && strings.Contains(lowerText, f.keyword))
		if !matched {
			continue
		}
		cooldownKey := fmt.Sprintf("%d:%s", chatID, f.keyword)
		if p.cooldownActive(cooldownKey) {
			break
		}

		peer := extractPeerInput(msg.PeerID, e)
		if peer == nil {
			return fmt.Errorf("filters: cannot resolve chat peer %d for reply", chatID)
		}
		if _, err := svc.SendMessage(ctx, peer, f.replyText); err != nil {
			return fmt.Errorf("filters: send reply for %q: %w", f.keyword, err)
		}
		p.markCooldown(cooldownKey)
		if decision := core.GetMessageDecision(ctx); decision != nil {
			decision.SetSuppressAFK(true)
		}
		break
	}
	return nil
}

func (p *Plugin) getCachedFilters(chatID int64) ([]compiledFilter, bool) {
	p.cacheMu.RLock()
	filters, ok := p.chatFilters[chatID]
	accessed := p.chatAccess[chatID]
	p.cacheMu.RUnlock()
	if !ok || time.Since(accessed) >= filterCacheTTL {
		return nil, false
	}
	p.cacheMu.Lock()
	p.chatAccess[chatID] = time.Now()
	p.cacheMu.Unlock()
	return filters, true
}

func (p *Plugin) cacheFilters(chatID int64, filters []compiledFilter) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	if len(p.chatFilters) >= 500 {
		var oldestChat int64
		var oldestTime time.Time
		for c, t := range p.chatAccess {
			if oldestTime.IsZero() || t.Before(oldestTime) {
				oldestTime, oldestChat = t, c
			}
		}
		if oldestChat != 0 {
			delete(p.chatFilters, oldestChat)
			delete(p.chatAccess, oldestChat)
		}
	}
	p.chatFilters[chatID] = filters
	p.chatAccess[chatID] = time.Now()
}

func (p *Plugin) cooldownActive(key string) bool {
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	last, exists := p.lastReply[key]
	return exists && time.Since(last) < filterCooldown
}

func (p *Plugin) markCooldown(key string) {
	now := time.Now()
	p.cooldownMu.Lock()
	p.lastReply[key] = now
	if len(p.lastReply) > 1000 {
		for k, v := range p.lastReply {
			if now.Sub(v) > 30*time.Second {
				delete(p.lastReply, k)
			}
		}
	}
	p.cooldownMu.Unlock()
}

func compileFilters(raw []Filter) []compiledFilter {
	res := make([]compiledFilter, len(raw))
	for i, f := range raw {
		res[i] = compileFilterItem(f)
	}
	return res
}

func compileFilterItem(f Filter) compiledFilter {
	kw := strings.ToLower(strings.TrimSpace(f.Keyword))
	var re *regexp.Regexp
	if kw != "" {
		pattern := `(?i)(?:^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(kw) + `(?:$|[^\p{L}\p{N}_])`
		re, _ = regexp.Compile(pattern)
	}
	return compiledFilter{keyword: kw, replyText: f.ReplyText, re: re}
}

func matchFilter(text, keyword string) bool {
	f := compileFilterItem(Filter{Keyword: keyword})
	lowerText := strings.ToLower(text)
	if f.re != nil {
		return f.re.MatchString(lowerText)
	}
	return strings.Contains(lowerText, f.keyword)
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
		if u, ok := e.Users[p.UserID]; ok && u != nil && u.AccessHash != 0 {
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
		if ch, ok := e.Channels[p.ChannelID]; ok && ch != nil && ch.AccessHash != 0 {
			return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: ch.AccessHash}
		}
		return nil
	}
	return nil
}
