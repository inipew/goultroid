package filters

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

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	filterCacheTTL          = 10 * time.Minute
	filterCooldown          = 5 * time.Second
	filterDeliveryTimeout   = 30 * time.Second
)

var filterDeliverySequence atomic.Uint64

var _ plugin.MessageEventPlugin = (*Plugin)(nil)
var _ plugin.MessageEventStatePlugin = (*Plugin)(nil)

type compiledFilter struct {
	keyword  string
	response savedresponse.Response
	re       *regexp.Regexp
}

type Plugin struct {
	db           Repository
	svcFunc      func() core.TelegramServicer
	responses    *savedresponse.Service
	tasks        tasks.Client
	featureState core.ChatFeatureSnapshot
	cacheMu      sync.RWMutex
	chatFilters  map[int64][]compiledFilter
	chatAccess   map[int64]time.Time
	cooldownMu   sync.Mutex
	lastReply    map[string]time.Time
}

func New(db Repository, svcFunc func() core.TelegramServicer, responses ...*savedresponse.Service) *Plugin {
	p := &Plugin{
		db: db, svcFunc: svcFunc,
		responses: savedresponse.NewService(nil),
		chatFilters: make(map[int64][]compiledFilter),
		chatAccess: make(map[int64]time.Time),
		lastReply: make(map[string]time.Time),
	}
	if len(responses) > 0 && responses[0] != nil {
		p.responses = responses[0]
	}
	return p
}

func (p *Plugin) Name() string { return "filters" }

func (p *Plugin) Init() error { return p.InitContext(context.Background()) }

func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	if p.responses == nil {
		return errors.New("filters: saved response service is not configured")
	}
	files, err := pctx.Files()
	if err != nil {
		return err
	}
	p.responses.SetFiles(files)

	client, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("filters: initialize task client: %w", err)
	}
	p.tasks = client
	return nil
}

func (p *Plugin) InitContext(ctx context.Context) error {
	if repo, ok := p.db.(ActiveChatRepository); ok {
		chatIDs, err := repo.ListActiveChatIDs(ctx)
		if err == nil {
			p.featureState.ReplaceLoaded(chatIDs)
		}
	}
	return nil
}

func (p *Plugin) MessageHookPriority() int { return 20 }

func (p *Plugin) MessageHookInterested(chatID int64) bool { return p.featureState.Interested(chatID) }

func (p *Plugin) MessageHookRouting() core.MessageHookRouting {
	return core.MessageHookRouting{
		Lane: core.MessageHookDecision,
		Interests: []core.MessageHookInterest{{
			Directions: core.MessageDirectionIncoming, Peers: core.MessagePeerStable,
			Commands: core.MessagePlain, RequireText: true,
		}},
	}
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name: "filter", Description: "Save a rich automated keyword filter in this chat",
			Usage: ".filter <keyword> <reply text> or reply to text/media with .filter <keyword>",
			Category: "Filters", Permission: core.PermissionSudo, GroupOnly: true,
			Resources: []tasks.ResourceRequirement{{Name: "download", Amount: 1}},
			Handler: p.handleFilter,
		},
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
		_ = ctx.EditOrReply("⚠️ Usage: <code>.filter &lt;keyword&gt; &lt;reply text&gt;</code> or reply to text/media with <code>.filter &lt;keyword&gt;</code>")
		return errors.New("missing arguments")
	}
	if p.db == nil || p.responses == nil {
		return errors.New("filters: persistence is unavailable")
	}
	keyword := strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	if keyword == "" {
		_ = ctx.EditOrReply("⚠️ Filter keyword cannot be empty.")
		return errors.New("empty filter keyword")
	}

	var response savedresponse.Response
	var err error
	if len(ctx.Args) >= 2 {
		response = savedresponse.NewText(strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0])))
	} else {
		response, err = p.responses.CaptureReply(ctx)
		if err != nil {
			_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Could not capture replied response: %v", err))
			return err
		}
	}
	if response.Empty() {
		_ = ctx.EditOrReply("⚠️ Filter response cannot be empty.")
		return errors.New("empty filter response")
	}
	if err := savedresponse.Validate(response); err != nil {
		_ = p.responses.DeleteMedia(ctx.Ctx, response)
		_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Invalid filter response: %v", err))
		return err
	}

	chatID := p.getChatID(ctx)
	p.featureState.MarkUnknown(chatID)
	previous, err := p.db.GetFilter(ctx.Ctx, chatID, keyword)
	if err != nil {
		_ = p.responses.DeleteMedia(ctx.Ctx, response)
		return err
	}
	var old savedresponse.Response
	if previous != nil {
		old = previous.Response
	}
	if err := p.responses.CommitReplacement(ctx.Ctx, old, response, func() error {
		return p.db.SaveFilter(ctx.Ctx, chatID, keyword, response)
	}); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to save filter: %v", err))
		return err
	}
	p.featureState.SetActive(chatID, true)
	p.invalidateChat(chatID)
	return ctx.EditOrReply(fmt.Sprintf("🎯 Filter <code>%s</code> saved successfully.", html.EscapeString(keyword)))
}

func (p *Plugin) handleStop(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.stop &lt;keyword&gt;</code>")
		return errors.New("missing filter keyword")
	}
	if p.db == nil || p.responses == nil {
		return errors.New("filters: persistence is unavailable")
	}
	keyword := strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	chatID := p.getChatID(ctx)
	filter, err := p.db.GetFilter(ctx.Ctx, chatID, keyword)
	if err != nil {
		return err
	}
	if filter == nil {
		_ = ctx.EditOrReply(fmt.Sprintf("ℹ️ Filter <code>%s</code> not found.", html.EscapeString(keyword)))
		return errors.New("filter not found")
	}

	p.featureState.MarkUnknown(chatID)
	if err := p.responses.CommitDelete(ctx.Ctx, filter.Response, func() error {
		return p.db.DeleteFilter(ctx.Ctx, chatID, keyword)
	}); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to stop filter: %v", err))
		return err
	}
	if remaining, err := p.db.ListFilters(ctx.Ctx, chatID); err != nil {
		p.featureState.MarkUnknown(chatID)
	} else {
		p.featureState.SetActive(chatID, len(remaining) > 0)
	}
	p.invalidateChat(chatID)
	return ctx.EditOrReply(fmt.Sprintf("🗑️ Filter <code>%s</code> stopped.", html.EscapeString(keyword)))
}

func (p *Plugin) handleList(ctx *core.Context) error {
	if p.db == nil {
		return errors.New("filters: database is unavailable")
	}
	list, err := p.db.ListFilters(ctx.Ctx, p.getChatID(ctx))
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return ctx.EditOrReply("ℹ️ No active filters in this chat.")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🎯 <b>Active Filters in this chat (%d):</b>\n", len(list))
	for _, f := range list {
		fmt.Fprintf(&sb, "• <code>%s</code>\n", html.EscapeString(f.Keyword))
	}
	return ctx.EditOrReply(sb.String())
}

func (p *Plugin) invalidateChat(chatID int64) {
	p.cacheMu.Lock()
	delete(p.chatFilters, chatID)
	delete(p.chatAccess, chatID)
	p.cacheMu.Unlock()
}

func (p *Plugin) HandleMessageEvent(ctx context.Context, message *core.MessageEnvelope) error {
	if message == nil || message.IsCommand || message.Text == "" || message.Outgoing {
		return nil
	}
	if decision := core.GetMessageDecision(ctx); decision != nil && (decision.IsSuppressedFilters() || decision.IsSuppressedAutomation()) {
		return nil
	}
	if message.Sender.IsBot || p.svcFunc == nil || p.db == nil || p.responses == nil {
		return nil
	}
	svc := p.svcFunc()
	if svc == nil || message.ChatID == 0 {
		return nil
	}

	filters, ok := p.getCachedFilters(message.ChatID)
	if !ok {
		rawFilters, err := p.db.ListFilters(ctx, message.ChatID)
		if err != nil {
			p.featureState.MarkUnknown(message.ChatID)
			return nil
		}
		p.featureState.SetActive(message.ChatID, len(rawFilters) > 0)
		filters = compileFilters(rawFilters)
		p.cacheFilters(message.ChatID, filters)
	}
	if len(filters) == 0 {
		return nil
	}

	lowerText := strings.ToLower(message.Text)
	for _, f := range filters {
		matched := (f.re != nil && f.re.MatchString(lowerText)) || (f.re == nil && f.keyword != "" && strings.Contains(lowerText, f.keyword))
		if !matched {
			continue
		}
		cooldownKey := fmt.Sprintf("%d:%s", message.ChatID, f.keyword)
		if p.cooldownActive(cooldownKey) {
			break
		}

		peer, err := message.Peer.InputPeer()
		if err != nil {
			return fmt.Errorf("filters: cannot resolve chat peer %d for reply: %w", message.ChatID, err)
		}
		vars := savedresponse.VarsFromEnvelope(message, time.Now())
		response := cloneSavedResponse(f.response)
		if err := p.submitDelivery(ctx, svc, peer, message.ChatID, message.ID, response, vars); err != nil {
			return fmt.Errorf("filters: submit reply for %q: %w", f.keyword, err)
		}
		p.markCooldown(cooldownKey)
		if decision := core.GetMessageDecision(ctx); decision != nil {
			decision.SetSuppressAFK(true)
		}
		break
	}
	return nil
}

func cloneSavedResponse(response savedresponse.Response) savedresponse.Response {
	cloned := response
	if response.Media != nil {
		media := *response.Media
		cloned.Media = &media
	}
	return cloned
}

func (p *Plugin) submitDelivery(
	admissionCtx context.Context,
	svc core.TelegramServicer,
	peer tg.InputPeerClass,
	chatID int64,
	messageID int,
	response savedresponse.Response,
	vars savedresponse.TemplateVars,
) error {
	if p.tasks == nil {
		return p.deliverResponse(admissionCtx, svc, peer, response, vars)
	}

	resources := []tasks.ResourceRequirement(nil)
	if response.Media != nil {
		resources = []tasks.ResourceRequirement{{Name: "media", Amount: 1}}
	}
	id := tasks.TaskID(fmt.Sprintf(
		"filter-response:%d:%d:%d",
		chatID,
		messageID,
		filterDeliverySequence.Add(1),
	))
	_, err := p.tasks.Submit(admissionCtx, tasks.WorkSpec{
		ID:               id,
		Pool:             tasks.PoolID("general"),
		Class:            tasks.PriorityNormal,
		OrderingKey:      fmt.Sprintf("chat:%d", chatID),
		ExecutionTimeout: filterDeliveryTimeout,
		Resources:        resources,
		Handler: func(taskCtx context.Context) error {
			return p.deliverResponse(taskCtx, svc, peer, response, vars)
		},
	})
	return err
}

func (p *Plugin) deliverResponse(
	ctx context.Context,
	svc core.TelegramServicer,
	peer tg.InputPeerClass,
	response savedresponse.Response,
	vars savedresponse.TemplateVars,
) error {
	if svc == nil || peer == nil {
		return errors.New("filters: telegram delivery is unavailable")
	}
	prepared, err := p.responses.Prepare(ctx, response, vars)
	if err != nil {
		return err
	}
	defer prepared.Cleanup()

	if prepared.MediaPath != "" {
		if _, err := svc.SendMedia(ctx, peer, prepared.MediaType, prepared.MediaPath, prepared.Caption); err != nil {
			return err
		}
	}
	if prepared.Text != "" {
		if _, err := svc.SendMessage(ctx, peer, prepared.Text); err != nil {
			return err
		}
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
		pattern := `(?:^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(kw) + `(?:$|[^\p{L}\p{N}_])`
		re, _ = regexp.Compile(pattern)
	}
	return compiledFilter{keyword: kw, response: f.Response, re: re}
}

func matchFilter(text, keyword string) bool {
	f := compileFilterItem(Filter{Keyword: keyword})
	lowerText := strings.ToLower(text)
	if f.re != nil {
		return f.re.MatchString(lowerText)
	}
	return strings.Contains(lowerText, f.keyword)
}
