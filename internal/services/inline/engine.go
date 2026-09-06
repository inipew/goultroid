package inline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/internal/ui"
	"go.uber.org/zap"
)

// Registry stores and resolves inline query handlers.
type Registry struct {
	handlers map[string]InlineHandler
	entries  []registryEntry
	mu       sync.RWMutex
}

type registryEntry struct {
	pattern string
	handler InlineHandler
	matcher InlineMatcher
	priority int
}

func matcherForPattern(pattern string) InlineMatcher {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "" {
		return nil // catch-all
	}
	// default exact keyword; prefix/regex via explicit matcher registration if needed
	return &exactKeywordMatcher{keyword: p}
}

type exactKeywordMatcher struct{ keyword string }

func (m *exactKeywordMatcher) Match(query string) ([]string, bool) {
	fields := strings.Fields(strings.TrimSpace(query))
	if len(fields) == 0 {
		return nil, false
	}
	if strings.ToLower(fields[0]) == m.keyword {
		return fields[1:], true
	}
	return nil, false
}

// NewRegistry creates a new Registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]InlineHandler),
	}
}

// Register registers an InlineHandler for its pattern or keyword.
func (r *Registry) Register(h InlineHandler) error {
	return r.RegisterWithPriority(h, 0)
}

// RegisterWithPriority registers with explicit priority (higher wins).
func (r *Registry) RegisterWithPriority(h InlineHandler, priority int) error {
	if h == nil {
		return fmt.Errorf("inline handler cannot be nil")
	}
	pattern := strings.ToLower(strings.TrimSpace(h.Pattern()))

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.handlers[pattern]; exists {
		return fmt.Errorf("inline handler for pattern %q is already registered", pattern)
	}

	var matcher InlineMatcher
	if v2, ok := h.(InlineHandlerV2); ok {
		matcher = v2.Matcher()
	}
	if matcher == nil {
		matcher = matcherForPattern(pattern)
	}

	r.handlers[pattern] = h
	r.entries = append(r.entries, registryEntry{pattern: pattern, handler: h, matcher: matcher, priority: priority})
	// sort by priority descending, then pattern length descending for determinism
	for i := len(r.entries) - 1; i > 0; i-- {
		if r.entries[i].priority > r.entries[i-1].priority || (r.entries[i].priority == r.entries[i-1].priority && len(r.entries[i].pattern) > len(r.entries[i-1].pattern)) {
			r.entries[i], r.entries[i-1] = r.entries[i-1], r.entries[i]
		} else {
			break
		}
	}
	return nil
}

// RegisterMatcher registers a handler with a custom matcher (prefix/regex).
func (r *Registry) RegisterMatcher(pattern string, matcher InlineMatcher, h InlineHandler, priority int) error {
	if h == nil || matcher == nil {
		return fmt.Errorf("matcher and handler cannot be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(pattern))
	if _, exists := r.handlers[key]; exists {
		return fmt.Errorf("inline handler for pattern %q is already registered", key)
	}
	r.handlers[key] = h
	r.entries = append(r.entries, registryEntry{pattern: key, handler: h, matcher: matcher, priority: priority})
	for i := len(r.entries) - 1; i > 0; i-- {
		if r.entries[i].priority > r.entries[i-1].priority {
			r.entries[i], r.entries[i-1] = r.entries[i-1], r.entries[i]
		} else {
			break
		}
	}
	return nil
}

// Resolve looks up the appropriate InlineHandler and splits query arguments.
func (r *Registry) Resolve(query string) (InlineHandler, []string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		if h, ok := r.handlers[""]; ok {
			return h, nil, true
		}
		return nil, nil, false
	}

	// Try entries in priority order where matcher matches
	for _, e := range r.entries {
		if e.pattern == "" {
			continue
		}
		if e.matcher != nil {
			if args, ok := e.matcher.Match(trimmed); ok {
				return e.handler, args, true
			}
		}
	}
	// Fallback keyword exact already covered via matcher, but keep legacy fast path
	fields := strings.Fields(trimmed)
	if len(fields) > 0 {
		kw := strings.ToLower(fields[0])
		if h, ok := r.handlers[kw]; ok {
			return h, fields[1:], true
		}
	}
	// catch-all
	if h, ok := r.handlers[""]; ok {
		return h, strings.Fields(trimmed), true
	}
	return nil, nil, false
}

// Engine coordinates inline query execution, caching, pagination, and MTProto serialization.
type Engine struct {
	registry  *Registry
	cache     *Cache
	paginator *Paginator
	logger    *zap.Logger
	cacheTime int
	metrics   core.MetricsCollector
	limiter   *ratelimit.Limiter
	timeout   time.Duration
	perms     *core.Permissions
}

const (
	defaultInlineTimeout   = 4 * time.Second
	maxInlineTitleLen      = 256
	maxInlineDescLen       = 512
	maxInlineTextLen       = 4096
	maxInlineIDLen         = 64
)

// NewEngine creates an initialized Engine.
func NewEngine(registry *Registry, logger *zap.Logger) *Engine {
	if registry == nil {
		registry = NewRegistry()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Engine{
		registry:  registry,
		cache:     NewCache(30 * time.Second),
		paginator: NewPaginator(DefaultPageSize),
		logger:    logger,
		cacheTime: 5, // 5 seconds Telegram cache
		timeout:   defaultInlineTimeout,
	}
}

// SetMetrics configures metrics collector.
func (e *Engine) SetMetrics(m core.MetricsCollector) { e.metrics = m }

// SetLimiter configures rate limiter for inline queries.
func (e *Engine) SetLimiter(l *ratelimit.Limiter) { e.limiter = l }

// SetTimeout configures per-handler timeout.
func (e *Engine) SetTimeout(d time.Duration) {
	if d > 0 {
		e.timeout = d
	}
}

// SetPermissions configures authorization checker for inline handlers.
func (e *Engine) SetPermissions(p *core.Permissions) { e.perms = p }

func isUserAllowed(policy InlineAccessPolicy, userID int64, perms *core.Permissions) bool {
	if policy.OwnerOnly {
		if perms == nil {
			return false
		}
		if !perms.IsOwner(userID) {
			return false
		}
	}
	if policy.SudoOnly {
		if perms == nil {
			return false
		}
		if !perms.IsSudo(userID) {
			return false
		}
	}
	if len(policy.AllowedUsers) > 0 {
		allowed := false
		for _, id := range policy.AllowedUsers {
			if id == userID {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	// AllowedChats requires chat context which inline query currently lacks; treat as permissive for now
	return true
}

// Registry returns the handler registry.
func (e *Engine) Registry() *Registry {
	return e.registry
}

// Cache returns the internal inline result cache.
func (e *Engine) Cache() *Cache {
	return e.cache
}

// Execute processes an incoming inline query and answers Telegram via the provided service.
func (e *Engine) Execute(ctx context.Context, svc core.TelegramServicer, queryID int64, userID int64, rawQuery string, offset string) error {
	start := time.Now()
	trimmed := strings.TrimSpace(rawQuery)
	correlationID := fmt.Sprintf("inline-%d-%d", queryID, time.Now().UnixNano())

	// Rate limiting per user (separate from command limiter)
	if e.limiter != nil {
		key := fmt.Sprintf("%d", userID)
		if !e.limiter.Allow(ratelimit.DimensionOperation, "inline:"+key) {
			e.logger.Debug("inline rate limited", zap.Int64("user_id", userID), zap.String("query", trimmed))
			if e.metrics != nil {
				e.metrics.RecordInline(false, 0, time.Since(start), fmt.Errorf("rate limited"))
			}
			if svc != nil {
				fallback := fallbackHelpResults("rate_limited")
				tgRes := serializeResults(fallback)
				_ = svc.AnswerInlineQueryOptions(ctx, queryID, tgRes, core.InlineAnswerOptions{NextOffset: "", CacheTime: 1})
			}
			return fmt.Errorf("%w: inline rate limited", core.ErrRateLimited)
		}
	}

	// Resolve handler first to know cache policy
	handler, args, ok := e.registry.Resolve(trimmed)
	if !ok {
		e.logger.Debug("no inline handler matched query", zap.String("query", trimmed), zap.String("correlation_id", correlationID))
		if e.metrics != nil {
			e.metrics.RecordInline(false, 0, time.Since(start), ErrNoMatchingHandler)
		}
		if svc != nil {
			fallback := fallbackHelpResults(trimmed)
			tgRes := serializeResults(fallback)
			_ = svc.AnswerInlineQueryOptions(ctx, queryID, tgRes, core.InlineAnswerOptions{NextOffset: "", CacheTime: 5})
		}
		return ErrNoMatchingHandler
	}

	// Determine cache policy
	policy := CacheGlobal
	var access InlineAccessPolicy
	if v2, ok := handler.(InlineHandlerV2); ok {
		policy = v2.CachePolicy()
		access = v2.AccessPolicy()
	}

	// Authorization before cache/handler execution (P2 auth policy)
	if access.OwnerOnly || access.SudoOnly || len(access.AllowedUsers) > 0 || len(access.AllowedChats) > 0 {
		if !isUserAllowed(access, userID, e.perms) {
			e.logger.Debug("inline unauthorized", zap.Int64("user_id", userID), zap.String("pattern", handler.Pattern()), zap.String("correlation_id", correlationID))
			if e.metrics != nil {
				e.metrics.RecordInline(false, 0, time.Since(start), fmt.Errorf("unauthorized"))
			}
			if svc != nil {
				fallback := fallbackUnauthorizedResults(handler.Pattern())
				tgRes := serializeResults(fallback)
				_ = svc.AnswerInlineQueryOptions(ctx, queryID, tgRes, core.InlineAnswerOptions{NextOffset: "", CacheTime: 1, SwitchPM: &tg.InlineBotSwitchPM{Text: "Unauthorized", StartParam: ""}})
			}
			return fmt.Errorf("unauthorized inline query")
		}
	}

	// Private results should not be cached globally; force per-user or none (audit P1 gallery/private)
	// Locale/version included in key per audit  handler/version + query + offset + user/chat/locale
	locale := "" // future: from InlineContext or user settings; empty for now keeps compat
	version := HandlerVersion(handler)
	if policy == CacheNone {
		// skip cache
	} else {
		// Check scoped cache with locale/version
		scopedKey := ScopedKeyEx(handler.Pattern(), trimmed, offset, policy, userID, 0, locale, version)
		if cached, hit := e.cache.GetScoped(scopedKey); hit {
			if e.metrics != nil {
				e.metrics.RecordInlineCacheHit()
				e.metrics.RecordInline(true, len(cached), time.Since(start), nil)
			}
			pageResults, nextOffset := e.paginator.Paginate(cached, offset)
			tgResults := serializeResults(pageResults)
			if svc != nil {
				// Use scoped cacheTime; for cached we reuse e.cacheTime
				err := svc.AnswerInlineQuery(ctx, queryID, tgResults, nextOffset, e.cacheTime)
				if err != nil && e.metrics != nil {
					e.metrics.RecordInline(false, len(tgResults), time.Since(start), err)
				}
				return err
			}
			return nil
		}
		if e.metrics != nil {
			e.metrics.RecordInlineCacheMiss()
		}
	}

	inlineCtx := &InlineContext{
		Ctx:           ctx,
		QueryID:       queryID,
		UserID:        userID,
		RawQuery:      trimmed,
		Pattern:       handler.Pattern(),
		Args:          args,
		Offset:        offset,
		CorrelationID: correlationID,
	}

	var resp *InlineResponse
	var results []InlineResult
	var handlerErr error

	// Timeout + panic recovery around handler (Fase 4 hardening)
	handlerErr = func() (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				e.logger.Error("inline handler panic", zap.Any("panic", rec), zap.String("pattern", handler.Pattern()), zap.String("correlation_id", correlationID))
				err = fmt.Errorf("%w: handler panic: %v", core.ErrInternal, rec)
			}
		}()
		timeout := e.timeout
		if timeout <= 0 {
			timeout = defaultInlineTimeout
		}
		hCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		inlineCtx.Ctx = hCtx
		if v2, ok := handler.(InlineHandlerV2); ok {
			resp, err = v2.HandleInlineV2(inlineCtx)
			if err == nil && resp != nil {
				results = resp.Results
			}
		} else {
			results, err = handler.HandleInline(inlineCtx)
			if err == nil {
				resp = &InlineResponse{Results: results, Cache: policy}
			}
		}
		return err
	}()
	if handlerErr != nil {
		e.logger.Warn("inline handler execution error", zap.Error(handlerErr), zap.String("correlation_id", correlationID), zap.String("pattern", handler.Pattern()))
		if e.metrics != nil {
			e.metrics.RecordInline(false, 0, time.Since(start), handlerErr)
		}
		if svc != nil {
			fallback := fallbackErrorResults(handlerErr)
			tgRes := serializeResults(fallback)
			_ = svc.AnswerInlineQueryOptions(ctx, queryID, tgRes, core.InlineAnswerOptions{NextOffset: "", CacheTime: 1})
		}
		return handlerErr
	}

	if resp == nil {
		resp = &InlineResponse{Results: results}
	}

	// Empty results fallback to help (Phase 3 UX: never answer empty)
	if len(resp.Results) == 0 {
		e.logger.Debug("inline handler returned empty results, using fallback help", zap.String("pattern", handler.Pattern()), zap.String("correlation_id", correlationID))
		resp.Results = fallbackEmptyResults(trimmed)
		// Help fallback should be cache-light
		if resp.CacheTime == 0 {
			resp.CacheTime = 5
		}
	}

	// Bounded result count (Telegram max 50)
	if len(resp.Results) > 50 {
		e.logger.Warn("inline results truncated to 50", zap.Int("original", len(resp.Results)), zap.String("correlation_id", correlationID))
		resp.Results = resp.Results[:50]
	}

	allResults := resp.Results

	// Cache full result set for materialized handlers if policy allows and not native paginated
	// Native pagination is signaled when handler provides NextOffset; then we don't paginate locally
	isNativePaginated := resp.NextOffset != ""
	var pageResults []InlineResult
	var nextOffset string
	if isNativePaginated {
		pageResults = allResults
		nextOffset = resp.NextOffset
	} else {
		// Fix P1 Private cache leak: private results must not be cached globally
		effectivePolicy := policy
		if resp.Private && effectivePolicy == CacheGlobal {
			effectivePolicy = CachePerUser
		}
		// cache then paginate with locale/version dimensions
		if effectivePolicy != CacheNone && len(allResults) > 0 {
			locale := "" // inlineCtx.Locale if set; empty keeps compat
			version := HandlerVersion(handler)
			scopedKey := ScopedKeyEx(handler.Pattern(), trimmed, "", effectivePolicy, userID, 0, locale, version)
			e.cache.SetScoped(scopedKey, allResults, 30*time.Second)
		}
		pageResults, nextOffset = e.paginator.Paginate(allResults, offset)
		// if handler provided NextOffset but also paginated, prefer handler's
		if resp.NextOffset != "" {
			nextOffset = resp.NextOffset
		}
	}

	tgResults := serializeResults(pageResults)

	if e.metrics != nil {
		e.metrics.RecordInline(false, len(tgResults), time.Since(start), nil)
	}

	if svc != nil {
		cacheTime := e.cacheTime
		if resp.CacheTime != 0 {
			cacheTime = resp.CacheTime
		}
		if resp.Private {
			// private results should not be cached server-side
			cacheTime = 0
		}
		opts := core.InlineAnswerOptions{
			Results:    tgResults,
			NextOffset: nextOffset,
			CacheTime:  cacheTime,
			Gallery:    resp.Gallery,
			Private:    resp.Private,
		}
		if resp.SwitchPM != nil {
			opts.SwitchPM = &tg.InlineBotSwitchPM{Text: resp.SwitchPM.Text, StartParam: resp.SwitchPM.Query}
		}
		if resp.SwitchWebView != nil {
			opts.SwitchWebView = &tg.InlineBotWebView{Text: resp.SwitchWebView.Text, URL: resp.SwitchWebView.URL}
		}
		// Prefer options API if available
		var ansErr error
		if svcWithOpts, ok := svc.(interface {
			AnswerInlineQueryOptions(context.Context, int64, []tg.InputBotInlineResultClass, core.InlineAnswerOptions) error
		}); ok {
			ansErr = svcWithOpts.AnswerInlineQueryOptions(ctx, queryID, tgResults, opts)
		} else {
			ansErr = svc.AnswerInlineQuery(ctx, queryID, tgResults, nextOffset, cacheTime)
		}
		if ansErr != nil && e.metrics != nil {
			// record answer error separately via RecordTelegram-like? reuse inline error
			e.metrics.RecordInline(false, len(tgResults), time.Since(start), ansErr)
		}
		return ansErr
	}
	return nil
}

func serializeResults(results []InlineResult) []tg.InputBotInlineResultClass {
	tgResults := make([]tg.InputBotInlineResultClass, 0, len(results))
	for _, res := range results {
		id := truncate(res.ID, maxInlineIDLen)
		if id == "" {
			id = "0"
		}
		title := truncate(res.Title, maxInlineTitleLen)
		desc := truncate(res.Description, maxInlineDescLen)
		text := truncate(res.Text, maxInlineTextLen)
		msg := &tg.InputBotInlineMessageText{
			Message: text,
		}
		if res.Markup != nil {
			if tgMarkup := res.Markup.ToTelegramMarkup(); tgMarkup != nil {
				msg.ReplyMarkup = tgMarkup
				msg.SetFlags()
			}
		}
		t := res.Type
		if t == "" {
			t = ResultArticle
		}
		// Typed serializer: use actual type string; generic InputBotInlineResult supports
		// article/photo/document etc via Type field. Dedicated InputBotInlineResultPhoto
		// requires InputPhotoClass (not web URL) so we keep generic path for now with correct Type.
		item := &tg.InputBotInlineResult{
			ID:          id,
			Type:        string(t),
			Title:       title,
			Description: desc,
			SendMessage: msg,
		}
		if res.ThumbURL != "" {
			item.SetThumb(tg.InputWebDocument{URL: truncate(res.ThumbURL, 512), MimeType: "image/jpeg"})
		}
		if res.URL != "" {
			item.SetURL(truncate(res.URL, 512))
		}
		// Content for media types via web document when MediaURL present
		if res.MediaURL != "" && t != ResultArticle {
			item.SetContent(tg.InputWebDocument{URL: truncate(res.MediaURL, 512), MimeType: res.MediaMimeType})
		}
		item.SetFlags()
		tgResults = append(tgResults, item)
	}
	return tgResults
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func fallbackErrorResults(err error) []InlineResult {
	msg := "⚠️ Unable to load results. Please try again."
	if err != nil && err.Error() != "" {
		// Don't leak internal details; keep generic msg
		_ = err
	}
	markup := ui.NewMarkup(ui.NewHelpSwitchRow("help"))
	return []InlineResult{{ID: "error", Type: ResultArticle, Title: "Error", Description: msg, Text: msg, Markup: &markup}}
}

func fallbackHelpResults(query string) []InlineResult {
	text := fmt.Sprintf("⚡ <b>GoUltroid Inline</b>\n\nNo handler for <code>%s</code>.\nTry <code>@bot help</code> or <code>@bot ping</code>.", query)
	if strings.TrimSpace(query) == "" {
		text = "⚡ <b>GoUltroid Inline Assistant</b>\n\nType <code>@bot help</code> to search commands or <code>@bot ping</code> for status."
	}
	markup := ui.NewMarkup(ui.ButtonRow{
		ui.NewSwitchInlineButton("🔍 Search Help", "help ", false),
		ui.NewSwitchInlineButton("🏓 Ping", "ping", false),
	})
	return []InlineResult{{ID: "fallback_help", Type: ResultArticle, Title: "GoUltroid Help", Description: "Search commands or check status", Text: text, Markup: &markup}}
}

func fallbackEmptyResults(query string) []InlineResult {
	text := fmt.Sprintf("🔍 No results for <code>%s</code>.\n\nTry different keywords or <code>@bot help</code>.", query)
	if strings.TrimSpace(query) == "" {
		text = "🔍 No results.\n\nTry <code>@bot help</code> to see available commands."
	}
	markup := ui.NewMarkup(ui.NewHelpSwitchRow("help"))
	return []InlineResult{{ID: "empty_help", Type: ResultArticle, Title: "No results", Description: "Try help or ping", Text: text, Markup: &markup}}
}

func fallbackUnauthorizedResults(pattern string) []InlineResult {
	text := "⛔ Unauthorized.\n\nThis inline command is restricted to owner/sudo."
	if pattern != "" {
		text = fmt.Sprintf("⛔ Unauthorized for <code>%s</code>.\n\nContact the bot owner.", pattern)
	}
	markup := ui.NewMarkup(ui.ButtonRow{ui.NewURLButton("Contact Owner", "https://t.me/")})
	return []InlineResult{{ID: "unauthorized", Type: ResultArticle, Title: "Unauthorized", Description: "Owner only", Text: text, Markup: &markup}}
}
