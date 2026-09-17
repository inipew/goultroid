package inline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
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
	pattern    string
	handler    InlineHandler
	matcher    InlineMatcher
	priority   int
	capability Capability
}

// DefinitionLease manages deterministic unregistration of an inline capability.
type DefinitionLease struct {
	def      Definition
	registry *Registry
	closed   atomic.Bool
}

// Close unregisters the inline capability from its registry.
func (l *DefinitionLease) Close() {
	if l == nil || !l.closed.CompareAndSwap(false, true) {
		return
	}
	if l.registry != nil {
		l.registry.unregister(l.def)
	}
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

// RegisterDefinition registers a complete inline capability definition with metadata and matcher.
func (r *Registry) RegisterDefinition(def Definition) (*DefinitionLease, error) {
	if def.Handler == nil {
		return nil, fmt.Errorf("inline handler cannot be nil")
	}
	pattern := strings.ToLower(strings.TrimSpace(def.Capability.Pattern))
	if pattern == "" {
		pattern = strings.ToLower(strings.TrimSpace(def.Handler.Pattern()))
		def.Capability.Pattern = pattern
	}

	if def.Capability.ID == "" {
		if pattern != "" {
			def.Capability.ID = CapabilityID(pattern)
		} else {
			def.Capability.ID = "default"
		}
	}
	if def.Capability.Title == "" {
		if pattern != "" {
			def.Capability.Title = strings.ToUpper(pattern[:1]) + pattern[1:]
		} else {
			def.Capability.Title = "Assistant"
		}
	}
	if def.Capability.Description == "" {
		def.Capability.Description = def.Handler.Description()
	}
	if def.Capability.Surfaces == 0 {
		def.Capability.Surfaces = execution.SurfaceInline
	}
	if def.Matcher == nil {
		if v2, ok := def.Handler.(InlineHandlerV2); ok {
			def.Matcher = v2.Matcher()
		}
		if def.Matcher == nil {
			def.Matcher = matcherForPattern(pattern)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.handlers[pattern]; exists {
		return nil, fmt.Errorf("inline handler for pattern %q is already registered", pattern)
	}

	r.handlers[pattern] = def.Handler
	r.entries = append(r.entries, registryEntry{
		pattern:    pattern,
		handler:    def.Handler,
		matcher:    def.Matcher,
		priority:   def.Capability.Priority,
		capability: def.Capability,
	})

	// sort by priority descending, then pattern length descending for determinism
	for i := len(r.entries) - 1; i > 0; i-- {
		if r.entries[i].priority > r.entries[i-1].priority || (r.entries[i].priority == r.entries[i-1].priority && len(r.entries[i].pattern) > len(r.entries[i-1].pattern)) {
			r.entries[i], r.entries[i-1] = r.entries[i-1], r.entries[i]
		} else {
			break
		}
	}

	return &DefinitionLease{
		def:      def,
		registry: r,
	}, nil
}

// Register registers an InlineHandler for its pattern or keyword (backward compatible wrapper).
func (r *Registry) Register(h InlineHandler) error {
	return r.RegisterWithPriority(h, 0)
}

// RegisterWithPriority registers with explicit priority (higher wins).
func (r *Registry) RegisterWithPriority(h InlineHandler, priority int) error {
	if h == nil {
		return fmt.Errorf("inline handler cannot be nil")
	}
	pattern := strings.ToLower(strings.TrimSpace(h.Pattern()))
	var access InlineAccessPolicy
	if ah, ok := h.(InlineAuthorizer); ok {
		access = ah.AccessPolicy()
	}
	var matcher InlineMatcher
	if v2, ok := h.(InlineHandlerV2); ok {
		matcher = v2.Matcher()
	}
	_, err := r.RegisterDefinition(Definition{
		Capability: Capability{
			ID:          CapabilityID(pattern),
			Pattern:     pattern,
			Title:       pattern,
			Description: h.Description(),
			Surfaces:    execution.SurfaceInline,
			Access:      access,
			Priority:    priority,
			Version:     1,
		},
		Matcher: matcher,
		Handler: h,
	})
	return err
}

// RegisterMatcher registers a handler with a custom matcher (prefix/regex).
func (r *Registry) RegisterMatcher(pattern string, matcher InlineMatcher, h InlineHandler, priority int) error {
	if h == nil || matcher == nil {
		return fmt.Errorf("matcher and handler cannot be nil")
	}
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	var access InlineAccessPolicy
	if ah, ok := h.(InlineAuthorizer); ok {
		access = ah.AccessPolicy()
	}
	_, err := r.RegisterDefinition(Definition{
		Capability: Capability{
			ID:          CapabilityID(pattern),
			Pattern:     pattern,
			Title:       pattern,
			Description: h.Description(),
			Surfaces:    execution.SurfaceInline,
			Access:      access,
			Priority:    priority,
			Version:     1,
		},
		Matcher: matcher,
		Handler: h,
	})
	return err
}

func (r *Registry) unregister(def Definition) {
	r.mu.Lock()
	defer r.mu.Unlock()

	pattern := strings.ToLower(strings.TrimSpace(def.Capability.Pattern))
	for i, e := range r.entries {
		if (def.Capability.ID != "" && e.capability.ID == def.Capability.ID) || e.pattern == pattern {
			r.entries = append(r.entries[:i], r.entries[i+1:]...)
			delete(r.handlers, pattern)
			break
		}
	}
}

// RevokeOwner unregisters all inline handlers belonging to a specific plugin owner and generation.
func (r *Registry) RevokeOwner(owner string, generation uint64) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	revoked := 0
	filtered := make([]registryEntry, 0, len(r.entries))
	for _, e := range r.entries {
		if e.capability.Owner == owner && (generation == 0 || e.capability.Generation == generation) {
			delete(r.handlers, e.pattern)
			revoked++
		} else {
			filtered = append(filtered, e)
		}
	}
	r.entries = filtered
	return revoked
}

// ListCapabilities discovers active inline capabilities filtered by caller permissions and search query.
func (r *Registry) ListCapabilities(query CatalogQuery) []Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []Capability
	search := strings.ToLower(strings.TrimSpace(query.Search))

	for _, e := range r.entries {
		cap := e.capability
		if cap.ID == "" {
			continue
		}

		if !query.IncludeAll {
			if cap.Hidden {
				continue
			}

			// Authorization enforcement
			if cap.Access.OwnerOnly && !query.Actor.IsOwner {
				continue
			}
			if cap.Access.SudoOnly && !query.Actor.IsOwner && !query.Actor.IsSudo {
				continue
			}
			if len(cap.Access.AllowedUsers) > 0 {
				allowed := false
				for _, uid := range cap.Access.AllowedUsers {
					if uid == query.Actor.UserID || query.Actor.IsOwner {
						allowed = true
						break
					}
				}
				if !allowed {
					continue
				}
			}
			if len(cap.Access.AllowedChatTypes) > 0 && query.PeerType != nil {
				peerMatch := false
				var currentChatType InlineChatType
				switch query.PeerType.(type) {
				case *tg.InlineQueryPeerTypePM:
					currentChatType = ChatTypePrivate
				case *tg.InlineQueryPeerTypeChat:
					currentChatType = ChatTypeGroup
				case *tg.InlineQueryPeerTypeMegagroup:
					currentChatType = ChatTypeSupergroup
				case *tg.InlineQueryPeerTypeBroadcast:
					currentChatType = ChatTypeChannel
				}
				for _, ct := range cap.Access.AllowedChatTypes {
					if ct == currentChatType {
						peerMatch = true
						break
					}
				}
				if !peerMatch {
					continue
				}
			}
		}

		if search != "" {
			match := strings.Contains(strings.ToLower(cap.Pattern), search) ||
				strings.Contains(strings.ToLower(cap.Title), search) ||
				strings.Contains(strings.ToLower(cap.Description), search)
			if !match {
				for _, kw := range cap.Keywords {
					if strings.Contains(strings.ToLower(kw), search) {
						match = true
						break
					}
				}
			}
			if !match {
				continue
			}
		}

		results = append(results, cap)
	}
	return results
}

// ResolveDefinition looks up the matched Definition with capability metadata and splits arguments.
func (r *Registry) ResolveDefinition(query string) (Definition, []string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		for _, e := range r.entries {
			if e.pattern == "" {
				return Definition{
					Capability: e.capability,
					Matcher:    e.matcher,
					Handler:    e.handler,
				}, nil, true
			}
		}
		if h, ok := r.handlers[""]; ok {
			return Definition{Handler: h}, nil, true
		}
		return Definition{}, nil, false
	}

	// Try entries in priority order where matcher matches
	for _, e := range r.entries {
		if e.pattern == "" {
			continue
		}
		if e.matcher != nil {
			if args, ok := e.matcher.Match(trimmed); ok {
				return Definition{
					Capability: e.capability,
					Matcher:    e.matcher,
					Handler:    e.handler,
				}, args, true
			}
		}
	}

	// Fallback keyword exact already covered via matcher, but keep legacy fast path
	fields := strings.Fields(trimmed)
	if len(fields) > 0 {
		kw := strings.ToLower(fields[0])
		for _, e := range r.entries {
			if e.pattern == kw {
				return Definition{
					Capability: e.capability,
					Matcher:    e.matcher,
					Handler:    e.handler,
				}, fields[1:], true
			}
		}
		if h, ok := r.handlers[kw]; ok {
			return Definition{Handler: h}, fields[1:], true
		}
	}

	// catch-all
	for _, e := range r.entries {
		if e.pattern == "" {
			return Definition{
				Capability: e.capability,
				Matcher:    e.matcher,
				Handler:    e.handler,
			}, strings.Fields(trimmed), true
		}
	}
	if h, ok := r.handlers[""]; ok {
		return Definition{Handler: h}, strings.Fields(trimmed), true
	}
	return Definition{}, nil, false
}

// Resolve looks up the appropriate InlineHandler and splits query arguments.
func (r *Registry) Resolve(query string) (InlineHandler, []string, bool) {
	def, args, ok := r.ResolveDefinition(query)
	return def.Handler, args, ok
}

// Engine coordinates inline query execution, caching, pagination, and MTProto serialization.
type Engine struct {
	registry    *Registry
	cache       *Cache
	paginator   *Paginator
	serializers *SerializerRegistry
	logger      *zap.Logger
	cacheTime   int
	metrics     core.MetricsCollector
	limiter     *ratelimit.Limiter
	timeout     time.Duration
	perms       *core.Permissions
}

const (
	defaultInlineTimeout = 4 * time.Second
	maxInlineTitleLen    = 256
	maxInlineDescLen     = 512
	maxInlineTextLen     = 4096
	maxInlineIDLen       = 64
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
		registry:    registry,
		cache:       NewCache(30 * time.Second),
		paginator:   NewPaginator(DefaultPageSize),
		serializers: NewSerializerRegistry(),
		logger:      logger,
		cacheTime:   5, // 5 seconds Telegram cache
		timeout:     defaultInlineTimeout,
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

// SetPaginator configures a custom paginator for inline results.
func (e *Engine) SetPaginator(p *Paginator) {
	if p != nil {
		e.paginator = p
	}
}

// Serializers returns the engine's serializer registry.
func (e *Engine) Serializers() *SerializerRegistry {
	return e.serializers
}

// SetSerializer registers a custom serializer for an inline result type.
func (e *Engine) SetSerializer(t InlineResultType, s ResultSerializer) {
	if e.serializers != nil {
		e.serializers.Register(t, s)
	}
}

// PeerTypeToChatType maps a tg.InlineQueryPeerTypeClass to an InlineChatType.
func PeerTypeToChatType(pt tg.InlineQueryPeerTypeClass) InlineChatType {
	if pt == nil {
		return ""
	}
	switch pt.(type) {
	case *tg.InlineQueryPeerTypeSameBotPM, *tg.InlineQueryPeerTypePM:
		return ChatTypePrivate
	case *tg.InlineQueryPeerTypeChat:
		return ChatTypeGroup
	case *tg.InlineQueryPeerTypeMegagroup:
		return ChatTypeSupergroup
	case *tg.InlineQueryPeerTypeBroadcast:
		return ChatTypeChannel
	default:
		return ""
	}
}

// hasCallbackButtons inspects inline results to detect whether any item contains inline keyboard buttons with callback_data.
func hasCallbackButtons(results []InlineResult) bool {
	for _, res := range results {
		if res.Markup != nil {
			for _, row := range res.Markup.Rows {
				for _, btn := range row {
					if btn.Type == ui.ButtonCallback || len(btn.Data) > 0 {
						return true
					}
				}
			}
		}
	}
	return false
}

func isUserAllowed(policy InlineAccessPolicy, userID int64, perms *core.Permissions, peerType tg.InlineQueryPeerTypeClass) bool {
	if policy.OwnerOnly {
		if perms == nil || !perms.IsOwner(userID) {
			return false
		}
	}
	if policy.SudoOnly {
		if perms == nil {
			return false
		}
		if !perms.IsOwner(userID) && !perms.IsSudo(userID) {
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
	// Fail-closed for AllowedChats: Telegram inline queries do not provide verifiable chat IDs.
	// A policy requiring specific chat IDs cannot be verified safely in inline mode.
	if len(policy.AllowedChats) > 0 {
		return false
	}
	// Check AllowedChatTypes against query PeerType
	if len(policy.AllowedChatTypes) > 0 {
		chatType := PeerTypeToChatType(peerType)
		if chatType == "" {
			return false
		}
		matched := false
		for _, ct := range policy.AllowedChatTypes {
			if ct == chatType {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
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
	return e.ExecuteWithPeerType(ctx, svc, queryID, userID, rawQuery, offset, nil)
}

// ExecuteWithPeerType processes an incoming inline query with peer context and answers Telegram.
func (e *Engine) ExecuteWithPeerType(ctx context.Context, svc core.TelegramServicer, queryID int64, userID int64, rawQuery string, offset string, peerType tg.InlineQueryPeerTypeClass) error {
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
				tgRes := e.serializeResults(fallback)
				_ = svc.AnswerInlineQueryOptions(ctx, queryID, tgRes, core.InlineAnswerOptions{NextOffset: "", CacheTime: 1})
			}
			return fmt.Errorf("%w: inline rate limited", core.ErrRateLimited)
		}
	}

	// Resolve handler and capability definition first
	def, args, ok := e.registry.ResolveDefinition(trimmed)
	if !ok {
		e.logger.Debug("no inline handler matched query", zap.String("query", trimmed), zap.String("correlation_id", correlationID))
		if e.metrics != nil {
			e.metrics.RecordInline(false, 0, time.Since(start), ErrNoMatchingHandler)
		}
		if svc != nil {
			fallback := fallbackHelpResults(trimmed)
			tgRes := e.serializeResults(fallback)
			_ = svc.AnswerInlineQueryOptions(ctx, queryID, tgRes, core.InlineAnswerOptions{NextOffset: "", CacheTime: 5})
		}
		return ErrNoMatchingHandler
	}

	handler := def.Handler
	policy := def.Capability.Cache
	access := def.Capability.Access

	// Fallback to InlineHandlerV2 if legacy interface was used and capability fields were unpopulated
	if v2, ok := handler.(InlineHandlerV2); ok {
		if policy == CacheGlobal && def.Capability.ID == "" {
			policy = v2.CachePolicy()
		}
		if !access.OwnerOnly && !access.SudoOnly && len(access.AllowedUsers) == 0 && len(access.AllowedChats) == 0 && len(access.AllowedChatTypes) == 0 {
			access = v2.AccessPolicy()
		}
	}

	// Authorization before cache/handler execution (P2 auth policy)
	if access.OwnerOnly || access.SudoOnly || len(access.AllowedUsers) > 0 || len(access.AllowedChats) > 0 || len(access.AllowedChatTypes) > 0 {
		if !isUserAllowed(access, userID, e.perms, peerType) {
			e.logger.Debug("inline unauthorized", zap.Int64("user_id", userID), zap.String("pattern", handler.Pattern()), zap.String("correlation_id", correlationID))
			if e.metrics != nil {
				e.metrics.RecordInline(false, 0, time.Since(start), fmt.Errorf("unauthorized"))
			}
			if svc != nil {
				fallback := fallbackUnauthorizedResults(handler.Pattern())
				tgRes := e.serializeResults(fallback)
				_ = svc.AnswerInlineQueryOptions(ctx, queryID, tgRes, core.InlineAnswerOptions{NextOffset: "", CacheTime: 1, SwitchPM: &tg.InlineBotSwitchPM{Text: "Unauthorized", StartParam: ""}})
			}
			return fmt.Errorf("unauthorized inline query")
		}
	}

	// Private results should not be cached globally; force per-user or none (audit P1 gallery/private)
	// Locale/version included in key per audit  handler/version + query + offset + user/chat/locale
	locale := "" // future: from InlineContext or user settings; empty for now keeps compat
	version := HandlerVersion(handler)
	if def.Capability.Version > 0 || def.Capability.Generation > 0 {
		version = fmt.Sprintf("%s-v%d-g%d", version, def.Capability.Version, def.Capability.Generation)
	}
	if policy != CacheNone {
		// Check scoped cache with locale/version.
		// The unpaginated result set is stored under key with offset "", so we look up with "".
		scopedKey := ScopedKeyEx(handler.Pattern(), trimmed, "", policy, userID, 0, locale, version)
		cached, hit := e.cache.GetScoped(scopedKey)
		if !hit && policy == CacheGlobal {
			// If declared as CacheGlobal, check if it was auto-downgraded to CachePerUser
			// (e.g. results contained callback buttons or private content).
			perUserKey := ScopedKeyEx(handler.Pattern(), trimmed, "", CachePerUser, userID, 0, locale, version)
			cached, hit = e.cache.GetScoped(perUserKey)
		}
		if hit {
			if e.metrics != nil {
				e.metrics.RecordInlineCacheHit()
				e.metrics.RecordInline(true, len(cached), time.Since(start), nil)
			}
			pageResults, nextOffset := e.paginator.Paginate(cached, offset)
			tgResults := e.serializeResults(pageResults)
			if len(tgResults) > 50 {
				tgResults = tgResults[:50]
			}
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
		PeerType:      peerType,
	}

	var resp *InlineResponse
	var results []InlineResult

	// Timeout + panic recovery around handler (Fase 4 hardening)
	handlerErr := func() (err error) {
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
			tgRes := e.serializeResults(fallback)
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
		// Fix P1 Private cache leak: private results or results containing interactive callback buttons
		// must not be cached globally to prevent cross-user state collision.
		effectivePolicy := policy
		if (resp.Private || hasCallbackButtons(allResults)) && effectivePolicy == CacheGlobal {
			effectivePolicy = CachePerUser
		}
		// cache then paginate with locale/version dimensions
		if effectivePolicy != CacheNone && len(allResults) > 0 {
			locale := "" // inlineCtx.Locale if set; empty keeps compat
			version := HandlerVersion(handler)
			if def.Capability.Version > 0 || def.Capability.Generation > 0 {
				version = fmt.Sprintf("%s-v%d-g%d", version, def.Capability.Version, def.Capability.Generation)
			}
			scopedKey := ScopedKeyEx(handler.Pattern(), trimmed, "", effectivePolicy, userID, 0, locale, version)
			e.cache.SetScoped(scopedKey, allResults, 30*time.Second)
		}
		pageResults, nextOffset = e.paginator.Paginate(allResults, offset)
		// if handler provided NextOffset but also paginated, prefer handler's
		if resp.NextOffset != "" {
			nextOffset = resp.NextOffset
		}
	}

	tgResults := e.serializeResults(pageResults)
	if len(tgResults) > 50 {
		tgResults = tgResults[:50]
	}

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

var defaultSerializers = NewSerializerRegistry()

func (e *Engine) serializeResults(results []InlineResult) []tg.InputBotInlineResultClass {
	if e != nil && e.serializers != nil {
		return e.serializers.SerializeAll(results)
	}
	return defaultSerializers.SerializeAll(results)
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
