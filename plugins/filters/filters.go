package filters

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

const (
	filterCacheTTL               = 10 * time.Minute
	filterCooldown               = 5 * time.Second
	filterCaptureTimeout         = 2 * time.Minute
	filterDeliveryTimeout        = 30 * time.Second
	filtersTelegramMessageRunes  = 4096
	MaxRulesPerChat              = 512
	MaxKeywordBytes              = 256
	MaxActiveChats               = 50_000
	maxCompiledFilterCacheChats  = 500
)

var filterTaskSequence atomic.Uint64

var ErrRuleLimit = fmt.Errorf("%w: filter rule limit exceeded", core.ErrResourceLimit)

var _ plugin.MessageEventPlugin = (*Plugin)(nil)
var _ plugin.MessageEventStatePlugin = (*Plugin)(nil)

type compiledFilter struct {
	keyword     string
	response    savedresponse.Response
	template    *savedresponse.CompiledTemplate
	templateErr error
}

type compiledFilterSet struct {
	filters  []compiledFilter
	matcher  *keywordMatcher
	revision uint64
}

type Plugin struct {
	db           Repository
	svcFunc      func() core.TelegramServicer
	responses    *savedresponse.Service
	delivery     *savedresponse.ResponseDelivery
	tasks        tasks.Client
	featureState core.ChatFeatureSnapshot
	ruleRevision atomic.Uint64
	cacheMu      sync.RWMutex
	chatFilters  map[int64]*compiledFilterSet
	chatAccess   map[int64]time.Time
	cooldownMu   sync.Mutex
	lastReply    map[string]time.Time
}

func New(db Repository, svcFunc func() core.TelegramServicer, responses ...*savedresponse.Service) *Plugin {
	responseService := savedresponse.NewService(nil)
	if len(responses) > 0 && responses[0] != nil {
		responseService = responses[0]
	}
	return &Plugin{
		db: db, svcFunc: svcFunc,
		responses:   responseService,
		delivery:    savedresponse.NewResponseDelivery(responseService),
		chatFilters: make(map[int64]*compiledFilterSet),
		chatAccess:  make(map[int64]time.Time),
		lastReply:   make(map[string]time.Time),
	}
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
		if err != nil {
			return fmt.Errorf("filters: preload active chats: %w", err)
		}
		p.featureState.ReplaceLoaded(chatIDs)
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
	surfaces := execution.SurfaceUserbot | execution.SurfaceAssistant
	auth := core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator}
	return []core.Command{
		{
			Name: "filter", Description: "Save a rich automated keyword filter in this chat",
			Usage: ".filter <keyword> <reply text> or reply to text/media with .filter <keyword>",
			Category: "Filters", Permission: core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization: auth, GroupOnly: true, Surfaces: surfaces,
			Handler: p.handleFilter,
		},
		{
			Name: "stop", Description: "Stop and delete a chat filter",
			Usage: ".stop <keyword>", Category: "Filters", Permission: core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization: auth, GroupOnly: true, Surfaces: surfaces,
			Handler: p.handleStop,
		},
		{
			Name: "filters", Description: "List all active filters in this chat",
			Category: "Filters", Permission: core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization: auth, GroupOnly: true, Surfaces: surfaces,
			Handler: p.handleList,
		},
		{
			Name: "filterinfo", Description: "Show filter response/media details",
			Usage: ".filterinfo <keyword>", Category: "Filters", Permission: core.PermissionSudo,
			AssistantPermission: core.PermissionRef(core.PermissionEveryone),
			GroupAuthorization: auth, GroupOnly: true, Surfaces: surfaces,
			Handler: p.handleInfo,
		},
	}
}

func (p *Plugin) getChatID(ctx *core.Context) int64 {
	if ctx.Chat != nil && ctx.Chat.ID != 0 {
		return ctx.Chat.ID
	}
	return ctx.SenderID()
}

func (p *Plugin) nextTaskID(kind string, chatID int64) tasks.TaskID {
	return tasks.TaskID(fmt.Sprintf(
		"filters:%s:%d:%d:%d",
		kind,
		chatID,
		time.Now().UnixNano(),
		filterTaskSequence.Add(1),
	))
}

func detachFilterContext(ctx *core.Context) *core.Context {
	if ctx == nil {
		return nil
	}
	cp := *ctx
	// Preserve Message, Svc, PeerID and the memoized reply needed by a queued
	// media capture, but release invocation-only/runtime references.
	cp.Ctx = nil
	cp.Args = nil
	cp.RawArgs = ""
	cp.Album = nil
	cp.Chat = nil
	cp.Sender = nil
	cp.Perms = nil
	cp.Principal = nil
	cp.Resolver = nil
	cp.Localizer = nil
	cp.EventBus = nil
	cp.DelayedActions = nil
	return &cp
}

func (p *Plugin) submitContinuation(
	admissionCtx context.Context,
	kind string,
	pool tasks.PoolID,
	chatID int64,
	timeout time.Duration,
	resources []tasks.ResourceRequirement,
	handler func(context.Context) error,
) error {
	if p.tasks == nil {
		return fmt.Errorf("%w: filters TaskEngine client is not configured", core.ErrUnavailable)
	}
	if admissionCtx == nil {
		admissionCtx = context.Background()
	}
	resources = append([]tasks.ResourceRequirement(nil), resources...)
	_, err := p.tasks.Submit(admissionCtx, tasks.WorkSpec{
		ID:               p.nextTaskID(kind, chatID),
		Pool:             pool,
		Class:            tasks.PriorityNormal,
		OrderingKey:      fmt.Sprintf("chat:%d", chatID),
		ExecutionTimeout: timeout,
		Resources:        resources,
		Handler: func(taskCtx context.Context) error {
			return handler(taskCtx)
		},
	})
	if err != nil {
		return fmt.Errorf("filters: submit %s continuation: %w", kind, err)
	}
	return nil
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
	if len(keyword) > MaxKeywordBytes {
		_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Filter keyword is too long (max %d bytes).", MaxKeywordBytes))
		return fmt.Errorf("%w: filter keyword exceeds %d bytes", core.ErrInvalidArgs, MaxKeywordBytes)
	}

	chatID := p.getChatID(ctx)
	if len(ctx.Args) >= 2 {
		response := savedresponse.NewText(strings.TrimSpace(strings.TrimPrefix(ctx.RawArgs, ctx.Args[0])))
		return p.saveFilterResponse(ctx, chatID, keyword, response)
	}

	reply, err := ctx.GetReply()
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Could not load replied response: %v", err))
		return err
	}
	return p.saveReply(ctx, chatID, keyword, reply)
}

func (p *Plugin) saveReply(ctx *core.Context, chatID int64, keyword string, reply *core.Message) error {
	if reply == nil {
		_ = ctx.EditOrReply("⚠️ Reply to text/media or provide filter response.")
		return savedresponse.ErrReplyNotFound
	}
	if !reply.HasMedia() {
		return p.saveFilterResponse(ctx, chatID, keyword, savedresponse.NewPlainText(reply.Text))
	}

	uiCtx := detachFilterContext(ctx)
	resources := []tasks.ResourceRequirement{{Name: "download", Amount: 1}}
	if err := p.submitContinuation(
		ctx.Ctx,
		"save-media",
		tasks.PoolID("download"),
		chatID,
		filterCaptureTimeout,
		resources,
		func(taskCtx context.Context) error {
			taskCore := uiCtx.WithContext(taskCtx)
			response, captureErr := p.responses.CaptureReply(taskCore)
			if captureErr != nil {
				_ = taskCore.EditOrReply(fmt.Sprintf("⚠️ Could not capture replied response: %v", captureErr))
				return captureErr
			}
			return p.saveFilterResponse(taskCore, chatID, keyword, response)
		},
	); err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to queue media filter save: %v", err))
		return err
	}
	return nil
}

func (p *Plugin) saveFilterResponse(ctx *core.Context, chatID int64, keyword string, response savedresponse.Response) error {
	if response.Empty() {
		_ = ctx.EditOrReply("⚠️ Filter response cannot be empty.")
		return errors.New("empty filter response")
	}
	if err := savedresponse.Validate(response); err != nil {
		_ = p.responses.DeleteMedia(ctx.Ctx, response)
		_ = ctx.EditOrReply(fmt.Sprintf("⚠️ Invalid filter response: %v", err))
		return err
	}

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
	p.ruleRevision.Add(1)
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
	p.ruleRevision.Add(1)
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
	return deliverFilterList(ctx, list)
}

func deliverFilterList(ctx *core.Context, filters []Filter) error {
	var current strings.Builder
	currentRunes := 0
	sent := false
	flush := func() error {
		if currentRunes == 0 {
			return nil
		}
		chunk := current.String()
		current.Reset()
		currentRunes = 0
		if !sent {
			sent = true
			return ctx.EditOrReply(chunk)
		}
		return ctx.Reply(chunk)
	}
	appendFragment := func(fragment string) error {
		for _, part := range core.SplitTelegramHTML(fragment, filtersTelegramMessageRunes) {
			partRunes := utf8.RuneCountInString(part)
			if currentRunes > 0 && currentRunes+partRunes > filtersTelegramMessageRunes {
				if err := flush(); err != nil {
					return err
				}
			}
			current.WriteString(part)
			currentRunes += partRunes
		}
		return nil
	}

	if err := appendFragment(fmt.Sprintf("🎯 <b>Active Filters in this chat (%d):</b>\n\n", len(filters))); err != nil {
		return err
	}
	for _, filter := range filters {
		if err := appendFragment(fmt.Sprintf(
			"• <code>[%s]</code> <code>%s</code>\n",
			html.EscapeString(filter.Response.Kind()),
			html.EscapeString(filter.Keyword),
		)); err != nil {
			return err
		}
	}
	if err := appendFragment("\nUse <code>.filterinfo &lt;keyword&gt;</code> for details."); err != nil {
		return err
	}
	return flush()
}

func (p *Plugin) handleInfo(ctx *core.Context) error {
	if p.db == nil {
		return errors.New("filters: database is unavailable")
	}
	if len(ctx.Args) == 0 {
		_ = ctx.EditOrReply("⚠️ Usage: <code>.filterinfo &lt;keyword&gt;</code>")
		return errors.New("missing filter keyword")
	}
	keyword := strings.ToLower(strings.TrimSpace(ctx.Args[0]))
	filter, err := p.db.GetFilter(ctx.Ctx, p.getChatID(ctx), keyword)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Error fetching filter info: %v", err))
		return err
	}
	if filter == nil {
		_ = ctx.EditOrReply(fmt.Sprintf("ℹ️ Filter <code>%s</code> not found.", html.EscapeString(keyword)))
		return errors.New("filter not found")
	}
	info, err := savedresponse.Inspect(filter.Response)
	if err != nil {
		_ = ctx.EditOrReply(fmt.Sprintf("❌ Failed to inspect filter: %v", err))
		return err
	}
	return ctx.EditOrReply(renderFilterInfo(filter, info))
}

func renderFilterInfo(filter *Filter, info savedresponse.Inspection) string {
	if filter == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🎯 <b>Filter Info</b>\n\n")
	fmt.Fprintf(&sb, "• <b>Keyword:</b> <code>%s</code>\n", html.EscapeString(filter.Keyword))
	fmt.Fprintf(&sb, "• <b>Type:</b> <code>%s</code>\n", html.EscapeString(info.Kind))
	fmt.Fprintf(&sb, "• <b>Format:</b> <code>%s</code>\n", html.EscapeString(string(info.Format)))
	if info.HasText {
		fmt.Fprintf(&sb, "• <b>Text/Caption:</b> <code>yes</code>\n")
	} else {
		fmt.Fprintf(&sb, "• <b>Text/Caption:</b> <code>no</code>\n")
	}
	fmt.Fprintf(&sb, "• <b>Template variables:</b> %s\n", renderFilterTemplateVariables(info.Variables))
	if info.Kind != "text" {
		if info.StickerFormat != "" {
			fmt.Fprintf(&sb, "• <b>Sticker format:</b> <code>%s</code>\n", html.EscapeString(info.StickerFormat))
		}
		if info.MediaName != "" {
			fmt.Fprintf(&sb, "• <b>Media:</b> <code>%s</code>\n", html.EscapeString(info.MediaName))
		}
		if info.MIMEType != "" {
			fmt.Fprintf(&sb, "• <b>MIME:</b> <code>%s</code>\n", html.EscapeString(info.MIMEType))
		}
	}
	if !filter.CreatedAt.IsZero() {
		fmt.Fprintf(&sb, "• <b>Last saved:</b> <code>%s</code>", filter.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return sb.String()
}

func renderFilterTemplateVariables(variables []string) string {
	if len(variables) == 0 {
		return "<code>none</code>"
	}
	parts := make([]string, 0, len(variables))
	for _, variable := range variables {
		parts = append(parts, fmt.Sprintf("<code>{%s}</code>", html.EscapeString(variable)))
	}
	return strings.Join(parts, ", ")
}

func (p *Plugin) invalidateChat(chatID int64) {
	p.cacheMu.Lock()
	delete(p.chatFilters, chatID)
	delete(p.chatAccess, chatID)
	p.cacheMu.Unlock()
}

func (p *Plugin) AssistantRuleInterested(chatID int64) bool {
	return p.MessageHookInterested(chatID)
}

func (p *Plugin) AssistantRuleRevision(_ int64) uint64 {
	return p.ruleRevision.Load()
}

func (p *Plugin) matchAssistantRule(
	ctx context.Context,
	message *core.MessageEnvelope,
) (*compiledFilter, bool, error) {
	if message == nil || message.IsCommand || message.Text == "" || message.Outgoing || message.Sender.IsBot {
		return nil, false, nil
	}
	if p.db == nil || p.responses == nil || message.ChatID == 0 {
		return nil, false, nil
	}

	filterSet, err := p.compiledFiltersForChat(ctx, message.ChatID)
	if err != nil {
		return nil, false, err
	}
	if filterSet == nil || len(filterSet.filters) == 0 {
		return nil, false, nil
	}

	matchIndex := filterSet.matcher.firstMatch(message.Text)
	if matchIndex < 0 {
		return nil, false, nil
	}
	f := &filterSet.filters[matchIndex]
	cooldownKey := fmt.Sprintf("%d:%s", message.ChatID, f.keyword)
	if p.cooldownActive(cooldownKey) {
		return nil, false, nil
	}
	if f.templateErr != nil {
		return nil, false, fmt.Errorf("filters: compile saved response for %q: %w", f.keyword, f.templateErr)
	}
	return f, true, nil
}

func (p *Plugin) MatchAssistantRule(ctx context.Context, message *core.MessageEnvelope) (bool, error) {
	_, matched, err := p.matchAssistantRule(ctx, message)
	return matched, err
}

func (p *Plugin) ApplyAssistantRule(
	ctx context.Context,
	svc core.TelegramServicer,
	message *core.MessageEnvelope,
) (bool, error) {
	f, matched, err := p.matchAssistantRule(ctx, message)
	if err != nil || !matched {
		return false, err
	}
	if svc == nil {
		return false, fmt.Errorf("%w: filter Assistant transport unavailable", core.ErrUnavailable)
	}
	peer, err := message.Peer.InputPeer()
	if err != nil {
		return false, fmt.Errorf("filters: cannot resolve chat peer %d for reply: %w", message.ChatID, err)
	}
	vars := savedresponse.VarsFromEnvelope(message, time.Now())
	response := f.response.Clone()
	if err := p.deliverResponse(ctx, svc, peer, response, f.template, vars); err != nil {
		return false, fmt.Errorf("filters: deliver Assistant response for %q: %w", f.keyword, err)
	}
	p.markCooldown(fmt.Sprintf("%d:%s", message.ChatID, f.keyword))
	return true, nil
}

func (p *Plugin) HandleMessageEvent(ctx context.Context, message *core.MessageEnvelope) error {
	if decision := core.GetMessageDecision(ctx); decision != nil &&
		(decision.IsSuppressedFilters() || decision.IsSuppressedAutomation()) {
		return nil
	}
	if p.svcFunc == nil {
		return nil
	}
	svc := p.svcFunc()
	if svc == nil {
		return nil
	}

	f, matched, err := p.matchAssistantRule(ctx, message)
	if err != nil || !matched {
		return err
	}
	peer, err := message.Peer.InputPeer()
	if err != nil {
		return fmt.Errorf("filters: cannot resolve chat peer %d for reply: %w", message.ChatID, err)
	}
	vars := savedresponse.VarsFromEnvelope(message, time.Now())
	response := f.response.Clone()
	if err := p.submitDelivery(ctx, svc, peer, message.ChatID, message.ID, response, f.template, vars); err != nil {
		return fmt.Errorf("filters: submit reply for %q: %w", f.keyword, err)
	}
	p.markCooldown(fmt.Sprintf("%d:%s", message.ChatID, f.keyword))
	if decision := core.GetMessageDecision(ctx); decision != nil {
		decision.SetSuppressAFK(true)
	}
	return nil
}

func (p *Plugin) submitDelivery(
	admissionCtx context.Context,
	svc core.TelegramServicer,
	peer tg.InputPeerClass,
	chatID int64,
	messageID int,
	response savedresponse.Response,
	template *savedresponse.CompiledTemplate,
	vars savedresponse.TemplateVars,
) error {
	if p.tasks == nil {
		return p.deliverResponse(admissionCtx, svc, peer, response, template, vars)
	}

	resources := []tasks.ResourceRequirement(nil)
	if response.HasMedia() {
		resources = []tasks.ResourceRequirement{{Name: "media", Amount: 1}}
	}
	return p.submitContinuation(
		admissionCtx,
		fmt.Sprintf("response-%d", messageID),
		tasks.PoolID("general"),
		chatID,
		filterDeliveryTimeout,
		resources,
		func(taskCtx context.Context) error {
			return p.deliverResponse(taskCtx, svc, peer, response, template, vars)
		},
	)
}

func (p *Plugin) deliverResponse(
	ctx context.Context,
	svc core.TelegramServicer,
	peer tg.InputPeerClass,
	response savedresponse.Response,
	template *savedresponse.CompiledTemplate,
	vars savedresponse.TemplateVars,
) error {
	if svc == nil || peer == nil {
		return errors.New("filters: telegram delivery is unavailable")
	}
	_, err := p.delivery.DeliverCompiled(ctx, response, template, vars, savedresponse.DeliverySink{
		SendMedia: func(mediaType, path, caption string) error {
			_, sendErr := svc.SendMedia(ctx, peer, mediaType, path, caption)
			return sendErr
		},
		SendText: func(text string) error {
			_, sendErr := svc.SendMessage(ctx, peer, text)
			return sendErr
		},
	})
	return err
}

func (p *Plugin) getCachedFilters(chatID int64, revision uint64) (*compiledFilterSet, bool) {
	p.cacheMu.RLock()
	filters, ok := p.chatFilters[chatID]
	accessed := p.chatAccess[chatID]
	p.cacheMu.RUnlock()
	if !ok || filters == nil || filters.revision != revision ||
		time.Since(accessed) >= filterCacheTTL {
		return nil, false
	}
	p.cacheMu.Lock()
	p.chatAccess[chatID] = time.Now()
	p.cacheMu.Unlock()
	return filters, true
}

func (p *Plugin) cacheFilters(chatID int64, filters *compiledFilterSet, revision uint64) bool {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	if p.ruleRevision.Load() != revision {
		return false
	}
	if len(p.chatFilters) >= maxCompiledFilterCacheChats {
		var oldestChat int64
		var oldestTime time.Time
		for c, accessed := range p.chatAccess {
			if oldestTime.IsZero() || accessed.Before(oldestTime) {
				oldestTime, oldestChat = accessed, c
			}
		}
		if oldestChat != 0 {
			delete(p.chatFilters, oldestChat)
			delete(p.chatAccess, oldestChat)
		}
	}
	if filters == nil {
		filters = &compiledFilterSet{}
	}
	filters.revision = revision
	p.chatFilters[chatID] = filters
	p.chatAccess[chatID] = time.Now()
	return true
}

func (p *Plugin) compiledFiltersForChat(
	ctx context.Context,
	chatID int64,
) (*compiledFilterSet, error) {
	for attempt := 0; attempt < 3; attempt++ {
		revision := p.ruleRevision.Load()
		if cached, ok := p.getCachedFilters(chatID, revision); ok {
			return cached, nil
		}

		rawFilters, err := p.db.ListFilters(ctx, chatID)
		if err != nil {
			p.featureState.MarkUnknown(chatID)
			return nil, err
		}
		if len(rawFilters) > MaxRulesPerChat {
			p.featureState.MarkUnknown(chatID)
			return nil, ErrRuleLimit
		}
		for _, filter := range rawFilters {
			if len(filter.Keyword) > MaxKeywordBytes {
				p.featureState.MarkUnknown(chatID)
				return nil, fmt.Errorf("%w: persisted filter keyword exceeds %d bytes", core.ErrResourceLimit, MaxKeywordBytes)
			}
			if err := savedresponse.Validate(filter.Response); err != nil {
				p.featureState.MarkUnknown(chatID)
				return nil, fmt.Errorf("filters: persisted response %q is invalid: %w", filter.Keyword, err)
			}
		}
		filterSet := compileFilterSet(rawFilters)
		if p.ruleRevision.Load() != revision {
			continue
		}
		p.featureState.SetActive(chatID, len(rawFilters) > 0)
		if p.cacheFilters(chatID, filterSet, revision) {
			return filterSet, nil
		}
	}
	return nil, fmt.Errorf("%w: filter rules changed during compilation", core.ErrConflict)
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

func compileFilterSet(raw []Filter) *compiledFilterSet {
	filters := compileFilters(raw)
	return &compiledFilterSet{
		filters: filters,
		matcher: newKeywordMatcher(filters),
	}
}

func compileFilterItem(f Filter) compiledFilter {
	kw := strings.ToLower(strings.TrimSpace(f.Keyword))
	response := f.Response.Clone()
	template, templateErr := savedresponse.Compile(response)
	return compiledFilter{
		keyword: kw, response: response,
		template: template, templateErr: templateErr,
	}
}

func matchFilter(text, keyword string) bool {
	filters := []compiledFilter{compileFilterItem(Filter{Keyword: keyword})}
	return newKeywordMatcher(filters).firstMatch(text) == 0
}
