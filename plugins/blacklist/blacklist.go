package blacklist

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
)

var _ plugin.MessageEventPlugin = (*Plugin)(nil)
var _ plugin.MessageEventStatePlugin = (*Plugin)(nil)

const (
	MaxRulesPerChat      = 512
	MaxRuleBytes         = 256
	MaxActiveChats       = 50_000
	maxCompiledCacheChats = 500
)

var ErrRuleLimit = fmt.Errorf("%w: blacklist rule limit exceeded", core.ErrResourceLimit)

type compiledBlacklist struct {
	word string
	re   *regexp.Regexp
}

type compiledBlacklistSet struct {
	items    []compiledBlacklist
	revision uint64
}

type Plugin struct {
	db            Repository
	svcFunc       func() core.TelegramServicer
	featureState  core.ChatFeatureSnapshot
	revisionSeq   atomic.Uint64
	cacheMu       sync.RWMutex
	chatRevision  map[int64]uint64
	chatBlacklist map[int64]compiledBlacklistSet
	chatAccess    map[int64]time.Time
}

func New(db Repository, svcFunc func() core.TelegramServicer) *Plugin {
	return &Plugin{
		db: db, svcFunc: svcFunc,
		chatRevision:  make(map[int64]uint64),
		chatBlacklist: make(map[int64]compiledBlacklistSet),
		chatAccess:    make(map[int64]time.Time),
	}
}
func (p *Plugin) Name() string { return "blacklist" }

func (p *Plugin) Init() error {
	return p.InitContext(context.Background())
}

func (p *Plugin) InitContext(ctx context.Context) error {
	if repo, ok := p.db.(ActiveChatRepository); ok {
		chatIDs, err := repo.ListActiveChatIDs(ctx)
		if err != nil {
			return fmt.Errorf("blacklist: preload active chats: %w", err)
		}
		p.featureState.ReplaceLoaded(chatIDs)
	}
	return nil
}

func (p *Plugin) MessageHookPriority() int { return 10 }

func (p *Plugin) MessageHookInterested(chatID int64) bool {
	return p.featureState.Interested(chatID)
}

func (p *Plugin) MessageHookRouting() core.MessageHookRouting {
	return core.MessageHookRouting{
		Lane: core.MessageHookDecision,
		Interests: []core.MessageHookInterest{{
			Directions:  core.MessageDirectionIncoming,
			Peers:       core.MessagePeerStable,
			Commands:    core.MessagePlain,
			RequireText: true,
		}},
	}
}

func (p *Plugin) Commands() []core.Command {
	surfaces := execution.SurfaceUserbot | execution.SurfaceAssistant
	manage := core.GroupAuthorizationRequirement{
		Level: core.GroupAuthorizationAdministrator,
		Rights: core.GroupAdminRights{DeleteMessages: true},
	}
	return []core.Command{
		{
			Name: "blacklist", Description: "Add a word or phrase to the chat blacklist for auto-deletion",
			Usage: ".blacklist <word/phrase>", Category: "Moderation", Permission: core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization: manage, GroupOnly: true, Surfaces: surfaces, Handler: p.handleBlacklist,
		},
		{
			Name: "unblacklist", Aliases: []string{"rmblacklist"},
			Description: "Remove a word or phrase from the chat blacklist",
			Usage: ".unblacklist <word/phrase>", Category: "Moderation", Permission: core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization: manage, GroupOnly: true, Surfaces: surfaces, Handler: p.handleUnblacklist,
		},
		{
			Name: "blacklists", Description: "List all blacklisted words in this chat",
			Category: "Moderation", Permission: core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization: core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator},
			GroupOnly: true, Surfaces: surfaces, Handler: p.handleListBlacklists,
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
	if len(word) > MaxRuleBytes {
		_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Blacklist rule is too long (max %d bytes).", MaxRuleBytes))
		return fmt.Errorf("%w: blacklist rule exceeds %d bytes", core.ErrInvalidArgs, MaxRuleBytes)
	}
	chatID := p.getChatID(ctx)
	p.featureState.MarkUnknown(chatID)
	if err := p.db.AddBlacklist(ctx.Ctx, chatID, word); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to add to blacklist: %v", err))
		return err
	}
	p.featureState.SetActive(chatID, true)
	p.invalidateChat(chatID, true)
	return ctx.EditOrReply(fmt.Sprintf("🚫 Added <code>%s</code> to chat blacklist.", html.EscapeString(word)))
}
func (p *Plugin) handleUnblacklist(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.unblacklist &lt;word/phrase&gt;</code>")
		return errors.New("missing blacklist word")
	}
	word := strings.ToLower(strings.TrimSpace(ctx.RawArgs))
	chatID := p.getChatID(ctx)
	p.featureState.MarkUnknown(chatID)
	if err := p.db.RemoveBlacklist(ctx.Ctx, chatID, word); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to remove from blacklist: %v", err))
		return err
	}
	active := false
	if remaining, err := p.db.ListBlacklists(ctx.Ctx, chatID); err != nil {
		p.featureState.MarkUnknown(chatID)
	} else {
		active = len(remaining) > 0
		p.featureState.SetActive(chatID, active)
	}
	p.invalidateChat(chatID, active)
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

func (p *Plugin) AssistantRuleInterested(chatID int64) bool {
	return p.MessageHookInterested(chatID)
}

func (p *Plugin) AssistantRuleRevision(chatID int64) uint64 {
	return p.chatRuleRevision(chatID)
}

func (p *Plugin) chatRuleRevision(chatID int64) uint64 {
	p.cacheMu.RLock()
	revision := p.chatRevision[chatID]
	p.cacheMu.RUnlock()
	return revision
}

func (p *Plugin) invalidateChat(chatID int64, active bool) {
	p.cacheMu.Lock()
	delete(p.chatBlacklist, chatID)
	delete(p.chatAccess, chatID)
	if active {
		p.chatRevision[chatID] = p.revisionSeq.Add(1)
	} else {
		delete(p.chatRevision, chatID)
	}
	p.cacheMu.Unlock()
}

func (p *Plugin) MatchAssistantRule(ctx context.Context, message *core.MessageEnvelope) (bool, error) {
	return p.matchMessage(ctx, message)
}

func (p *Plugin) ApplyAssistantRule(
	ctx context.Context,
	svc core.TelegramServicer,
	message *core.MessageEnvelope,
) (bool, error) {
	matched, err := p.matchMessage(ctx, message)
	if err != nil || !matched {
		return false, err
	}
	peer, err := message.Peer.InputPeer()
	if err != nil {
		return false, fmt.Errorf("blacklist: cannot resolve peer for chat %d; message %d was not deleted: %w",
			message.ChatID, message.ID, err)
	}
	if svc == nil {
		return false, fmt.Errorf("%w: blacklist Assistant transport unavailable", core.ErrUnavailable)
	}
	if err := svc.DeleteMessage(ctx, peer, []int{message.ID}); err != nil {
		return false, fmt.Errorf("blacklist: failed to delete message %d: %w", message.ID, err)
	}
	return true, nil
}

func (p *Plugin) compiledForChat(
	ctx context.Context,
	chatID int64,
) ([]compiledBlacklist, error) {
	for attempt := 0; attempt < 3; attempt++ {
		revision := p.chatRuleRevision(chatID)
		p.cacheMu.RLock()
		cached, ok := p.chatBlacklist[chatID]
		p.cacheMu.RUnlock()
		if ok && cached.revision == revision {
			p.cacheMu.Lock()
			p.chatAccess[chatID] = time.Now()
			p.cacheMu.Unlock()
			return cached.items, nil
		}

		rawWords, err := p.db.ListBlacklists(ctx, chatID)
		if err != nil {
			p.featureState.MarkUnknown(chatID)
			return nil, err
		}
		if len(rawWords) > MaxRulesPerChat {
			p.featureState.MarkUnknown(chatID)
			return nil, ErrRuleLimit
		}
		for _, word := range rawWords {
			if len(word) > MaxRuleBytes {
				p.featureState.MarkUnknown(chatID)
				return nil, fmt.Errorf("%w: persisted blacklist rule exceeds %d bytes", core.ErrResourceLimit, MaxRuleBytes)
			}
		}
		items := compileBlacklist(rawWords)
		if p.chatRuleRevision(chatID) != revision {
			continue
		}

		p.featureState.SetActive(chatID, len(rawWords) > 0)
		p.cacheMu.Lock()
		if p.chatRuleRevision(chatID) != revision {
			p.cacheMu.Unlock()
			continue
		}
		if len(p.chatBlacklist) >= maxCompiledCacheChats {
			var oldestChat int64
			var oldestTime time.Time
			for c, accessed := range p.chatAccess {
				if oldestTime.IsZero() || accessed.Before(oldestTime) {
					oldestTime, oldestChat = accessed, c
				}
			}
			if oldestChat != 0 {
				delete(p.chatBlacklist, oldestChat)
				delete(p.chatAccess, oldestChat)
			}
		}
		p.chatBlacklist[chatID] = compiledBlacklistSet{
			items:    items,
			revision: revision,
		}
		p.chatAccess[chatID] = time.Now()
		p.cacheMu.Unlock()
		return items, nil
	}
	return nil, fmt.Errorf("%w: blacklist rules changed during compilation", core.ErrConflict)
}

func (p *Plugin) matchMessage(ctx context.Context, message *core.MessageEnvelope) (bool, error) {
	if message == nil || message.IsCommand || message.Text == "" || message.Outgoing || message.Sender.IsBot {
		return false, nil
	}
	if p.db == nil || message.ChatID == 0 {
		return false, nil
	}
	items, err := p.compiledForChat(ctx, message.ChatID)
	if err != nil {
		return false, err
	}
	if len(items) == 0 {
		return false, nil
	}
	lowerText := strings.ToLower(message.Text)
	for _, b := range items {
		if (b.re != nil && b.re.MatchString(lowerText)) || (b.re == nil && b.word != "" && strings.Contains(lowerText, b.word)) {
			return true, nil
		}
	}
	return false, nil
}

func (p *Plugin) HandleMessageEvent(ctx context.Context, message *core.MessageEnvelope) error {
	if p.svcFunc == nil {
		return nil
	}
	svc := p.svcFunc()
	if svc == nil {
		return nil
	}
	handled, err := p.ApplyAssistantRule(ctx, svc, message)
	if err != nil {
		return err
	}
	if !handled {
		return nil
	}
	if decision := core.GetMessageDecision(ctx); decision != nil {
		decision.SetHandled(true)
		decision.SetSuppressAutomation(true)
		decision.SetSuppressAFK(true)
		decision.SetSuppressFilters(true)
		decision.SetSuppressCommands(true)
	}
	return core.ErrInterceptHandled
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
