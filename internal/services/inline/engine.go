package inline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/ratelimit"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/ui"
	"go.uber.org/zap"
)

// Registry stores and resolves inline query handlers.
type Registry struct {
	handlers map[string]registryEntry
	entries  []registryEntry
	next     uint64
	mu       sync.RWMutex
}

type registryEntry struct {
	pattern       string
	handler       InlineHandler
	matcher       InlineMatcher
	priority      int
	featureID     string
	interactionID string
	scope         tasks.ScopeIdentity
	token         uint64
}

// Binding connects one FeatureSpec inline interaction to its implementation.
// InteractionID is the stable catalog identity; Handler.Pattern controls query
// matching and may intentionally differ from that identity.
type Binding struct {
	InteractionID string
	Handler       InlineHandler
	Priority      int
}

// Registration owns one registry entry. Close is generation-safe: stale
// cleanup cannot remove a replacement registered for the same query pattern.
type Registration struct {
	registry *Registry
	pattern  string
	token    uint64
	once     sync.Once
}

// Resolved identifies one matched inline handler and its lifecycle ownership.
type Resolved struct {
	Handler       InlineHandler
	Args          []string
	FeatureID     string
	InteractionID string
	Scope         tasks.ScopeIdentity
	pattern       string
	token         uint64
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
		handlers: make(map[string]registryEntry),
	}
}

// Register registers a legacy unowned InlineHandler for its pattern or keyword.
func (r *Registry) Register(h InlineHandler) error {
	_, err := r.register("", "", tasks.ScopeIdentity{}, "", h, nil, 0)
	return err
}

// RegisterWithPriority registers a legacy unowned handler with explicit priority.
func (r *Registry) RegisterWithPriority(h InlineHandler, priority int) error {
	_, err := r.register("", "", tasks.ScopeIdentity{}, "", h, nil, priority)
	return err
}

// RegisterOwned binds a handler to one feature generation and interaction.
// Plugin lifecycle code should prefer this over the legacy registration APIs.
func (r *Registry) RegisterOwned(featureID, interactionID string, scope tasks.ScopeIdentity, h InlineHandler, priority int) (*Registration, error) {
	featureID = strings.ToLower(strings.TrimSpace(featureID))
	interactionID = strings.ToLower(strings.TrimSpace(interactionID))
	if featureID == "" || interactionID == "" || scope.IsZero() {
		return nil, fmt.Errorf("owned inline handler requires feature, interaction, and scope")
	}
	return r.register(featureID, interactionID, scope, "", h, nil, priority)
}

// RegisterMatcher registers a legacy unowned handler with a custom matcher.
func (r *Registry) RegisterMatcher(pattern string, matcher InlineMatcher, h InlineHandler, priority int) error {
	if h == nil || matcher == nil {
		return fmt.Errorf("matcher and handler cannot be nil")
	}
	_, err := r.register("", "", tasks.ScopeIdentity{}, pattern, h, matcher, priority)
	return err
}

func (r *Registry) register(featureID, interactionID string, scope tasks.ScopeIdentity, patternOverride string, h InlineHandler, matcher InlineMatcher, priority int) (*Registration, error) {
	if r == nil || h == nil {
		return nil, fmt.Errorf("inline handler cannot be nil")
	}
	pattern := strings.ToLower(strings.TrimSpace(patternOverride))
	if pattern == "" {
		pattern = strings.ToLower(strings.TrimSpace(h.Pattern()))
	}
	if matcher == nil {
		if v2, ok := h.(InlineHandlerV2); ok {
			matcher = v2.Matcher()
		}
	}
	if matcher == nil {
		matcher = matcherForPattern(pattern)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[pattern]; exists {
		return nil, fmt.Errorf("inline handler for pattern %q is already registered", pattern)
	}
	r.next++
	entry := registryEntry{
		pattern: pattern, handler: h, matcher: matcher, priority: priority,
		featureID: featureID, interactionID: interactionID, scope: scope, token: r.next,
	}
	r.handlers[pattern] = entry
	r.entries = append(r.entries, entry)
	r.sortEntriesLocked()
	return &Registration{registry: r, pattern: pattern, token: entry.token}, nil
}

func (r *Registry) sortEntriesLocked() {
	for i := 1; i < len(r.entries); i++ {
		for j := i; j > 0; j-- {
			left, right := r.entries[j-1], r.entries[j]
			if right.priority < left.priority || (right.priority == left.priority && len(right.pattern) <= len(left.pattern)) {
				break
			}
			r.entries[j-1], r.entries[j] = right, left
		}
	}
}

// Close removes only the exact registration generation.
func (r *Registration) Close() {
	if r == nil || r.registry == nil {
		return
	}
	r.once.Do(func() {
		registry := r.registry
		registry.mu.Lock()
		defer registry.mu.Unlock()
		current, ok := registry.handlers[r.pattern]
		if !ok || current.token != r.token {
			return
		}
		delete(registry.handlers, r.pattern)
		for i := range registry.entries {
			if registry.entries[i].token == r.token {
				registry.entries = append(registry.entries[:i], registry.entries[i+1:]...)
				break
			}
		}
	})
}

// Resolve looks up the appropriate InlineHandler and splits query arguments.
func (r *Registry) Resolve(query string) (InlineHandler, []string, bool) {
	resolved, ok := r.ResolveOwned(query)
	if !ok {
		return nil, nil, false
	}
	return resolved.Handler, resolved.Args, true
}

// ResolveOwned returns handler metadata including lifecycle scope.
func (r *Registry) ResolveOwned(query string) (Resolved, bool) {
	if r == nil {
		return Resolved{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		if entry, ok := r.handlers[""]; ok {
			return resolvedEntry(entry, nil), true
		}
		return Resolved{}, false
	}

	for _, entry := range r.entries {
		if entry.pattern == "" {
			continue
		}
		if entry.matcher != nil {
			if args, ok := entry.matcher.Match(trimmed); ok {
				return resolvedEntry(entry, args), true
			}
		}
	}
	fields := strings.Fields(trimmed)
	if len(fields) > 0 {
		if entry, ok := r.handlers[strings.ToLower(fields[0])]; ok {
			return resolvedEntry(entry, fields[1:]), true
		}
	}
	if entry, ok := r.handlers[""]; ok {
		return resolvedEntry(entry, strings.Fields(trimmed)), true
	}
	return Resolved{}, false
}

func resolvedEntry(entry registryEntry, args []string) Resolved {
	return Resolved{
		Handler: entry.handler, Args: append([]string(nil), args...),
		FeatureID: entry.featureID, InteractionID: entry.interactionID,
		Scope: entry.scope, pattern: entry.pattern, token: entry.token,
	}
}

// IsCurrent reports whether a previously resolved registration is still active.
func (r *Registry) IsCurrent(resolved Resolved) bool {
	if r == nil || resolved.Handler == nil || resolved.token == 0 {
		return false
	}
	pattern := resolved.pattern
	if pattern == "" {
		pattern = strings.ToLower(strings.TrimSpace(resolved.Handler.Pattern()))
	}
	r.mu.RLock()
	current, ok := r.handlers[pattern]
	r.mu.RUnlock()
	return ok && current.token == resolved.token
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
	sessions    *rootinteraction.Runtime
	compiler    *presentation.Compiler
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

// SetInteractionRuntime connects typed inline actions to the canonical a2
// session runtime. Query execution remains usable without it for non-interactive
// legacy results.
func (e *Engine) SetInteractionRuntime(runtime *rootinteraction.Runtime) {
	if e == nil {
		return
	}
	e.sessions = runtime
	if runtime == nil {
		e.compiler = nil
		return
	}
	e.compiler = presentation.NewCompiler(runtime)
}

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
func resultHasRawCallbackMarkup(result InlineResult) bool {
	if result.Markup == nil {
		return false
	}
	for _, row := range result.Markup.Rows {
		for _, button := range row {
			if button.Type == ui.ButtonCallback || len(button.Data) > 0 {
				return true
			}
		}
	}
	return false
}

func mergeCompiledActionRows(markup *ui.Markup, rows []presentation.CompiledRow) *ui.Markup {
	var merged ui.Markup
	if markup != nil {
		merged = *markup
		merged.Rows = make([]ui.ButtonRow, len(markup.Rows))
		for i, row := range markup.Rows {
			merged.Rows[i] = append(ui.ButtonRow(nil), row...)
			for j := range merged.Rows[i] {
				merged.Rows[i][j].Data = append([]byte(nil), row[j].Data...)
			}
		}
	}
	for _, row := range rows {
		out := make(ui.ButtonRow, 0, len(row))
		for _, button := range row {
			out = append(out, ui.NewCallbackButton(button.Text, button.Data))
		}
		if len(out) > 0 {
			merged.Rows = append(merged.Rows, out)
		}
	}
	if len(merged.Rows) == 0 {
		return nil
	}
	return &merged
}

func (e *Engine) compileTypedActions(
	ctx context.Context,
	resolved Resolved,
	userID int64,
	results []InlineResult,
) ([]InlineResult, []string, bool, error) {
	if len(results) == 0 {
		return results, nil, false, nil
	}
	out := cloneInlineResults(results)
	created := make([]string, 0)
	interactive := false
	cleanup := func() {
		if e == nil || e.sessions == nil {
			return
		}
		for _, id := range created {
			e.sessions.Cancel(id)
		}
	}
	for i := range out {
		result := &out[i]
		if resolved.FeatureID != "" && resultHasRawCallbackMarkup(*result) {
			cleanup()
			return nil, nil, false, fmt.Errorf("feature-owned inline result %q embeds raw callback data", result.ID)
		}
		if len(result.ActionRows) == 0 {
			continue
		}
		interactive = true
		if resolved.FeatureID == "" || resolved.Scope.IsZero() {
			cleanup()
			return nil, nil, false, fmt.Errorf("typed inline actions require feature ownership")
		}
		if e.sessions == nil || e.compiler == nil {
			cleanup()
			return nil, nil, false, fmt.Errorf("typed inline actions require interaction runtime")
		}
		session, err := e.sessions.Create(ctx, rootinteraction.CreateRequest{
			FeatureID: resolved.FeatureID,
			Binding:   rootinteraction.Binding{ActorID: userID},
			State:     append([]byte(nil), result.InteractionState...),
			TTL:       result.InteractionTTL,
		})
		if err != nil {
			cleanup()
			return nil, nil, false, err
		}
		created = append(created, session.Session.ID)
		compiled, err := e.compiler.CompileRows(ctx, session.Session.ID, result.ActionRows)
		if err != nil {
			cleanup()
			return nil, nil, false, err
		}
		result.Markup = mergeCompiledActionRows(result.Markup, compiled)
		result.ActionRows = nil
		result.InteractionState = nil
		result.InteractionTTL = 0
	}
	return out, created, interactive, nil
}

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

// PreparedQuery freezes one registry match at admission time. Scope is carried
// into TaskEngine so disable/reload can cancel queued or running feature work.
type PreparedQuery struct {
	query    string
	resolved Resolved
}

func (p PreparedQuery) Scope() tasks.ScopeIdentity { return p.resolved.Scope }

func (p PreparedQuery) Query() string { return p.query }

// Prepare resolves a query without executing feature code.
func (e *Engine) Prepare(rawQuery string) (PreparedQuery, error) {
	if e == nil || e.registry == nil {
		return PreparedQuery{}, ErrNoMatchingHandler
	}
	trimmed := strings.TrimSpace(rawQuery)
	resolved, ok := e.registry.ResolveOwned(trimmed)
	if !ok {
		return PreparedQuery{}, ErrNoMatchingHandler
	}
	return PreparedQuery{query: trimmed, resolved: resolved}, nil
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
	return e.executeWithPeerType(ctx, svc, queryID, userID, rawQuery, offset, peerType, nil)
}

// ExecutePreparedWithPeerType executes an admission-time match and rejects it if
// plugin lifecycle replaced or removed the registration before execution.
func (e *Engine) ExecutePreparedWithPeerType(ctx context.Context, svc core.TelegramServicer, queryID int64, userID int64, prepared PreparedQuery, offset string, peerType tg.InlineQueryPeerTypeClass) error {
	return e.executeWithPeerType(ctx, svc, queryID, userID, prepared.query, offset, peerType, &prepared.resolved)
}

func (e *Engine) executeWithPeerType(ctx context.Context, svc core.TelegramServicer, queryID int64, userID int64, rawQuery string, offset string, peerType tg.InlineQueryPeerTypeClass, prepared *Resolved) error {
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

	// Resolve handler first to know cache policy. Prepared feature work must
	// still point at the exact same registration generation.
	var resolved Resolved
	var ok bool
	if prepared != nil {
		if !e.registry.IsCurrent(*prepared) {
			return ErrStaleHandler
		}
		resolved = *prepared
		ok = true
	} else {
		resolved, ok = e.registry.ResolveOwned(trimmed)
	}
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
	handler := resolved.Handler
	args := resolved.Args

	// Determine cache policy
	policy := CacheGlobal
	var access InlineAccessPolicy
	if v2, ok := handler.(InlineHandlerV2); ok {
		policy = v2.CachePolicy()
		access = v2.AccessPolicy()
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
				// Cached responses cannot contain typed a2 interactions because
				// interactive responses are always CacheNone.
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if prepared != nil && !e.registry.IsCurrent(*prepared) {
			return ErrStaleHandler
		}
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

	interactiveResults := false
	for _, result := range resp.Results {
		if len(result.ActionRows) > 0 {
			interactiveResults = true
			break
		}
	}
	if interactiveResults {
		// a2 tokens are actor/session specific and revisions can change after a
		// callback. Never retain them in local or Telegram shared caches.
		policy = CacheNone
		resp.Private = true
		resp.CacheTime = 0
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
			scopedKey := ScopedKeyEx(handler.Pattern(), trimmed, "", effectivePolicy, userID, 0, locale, version)
			e.cache.SetScoped(scopedKey, allResults, 30*time.Second)
		}
		pageResults, nextOffset = e.paginator.Paginate(allResults, offset)
		// if handler provided NextOffset but also paginated, prefer handler's
		if resp.NextOffset != "" {
			nextOffset = resp.NextOffset
		}
	}

	interactionSessionsCommitted := false
	var interactionSessions []string
	if interactiveResults {
		compiledPage, created, _, compileErr := e.compileTypedActions(ctx, resolved, userID, pageResults)
		if compileErr != nil {
			e.logger.Warn("inline typed action compilation failed", zap.Error(compileErr), zap.String("correlation_id", correlationID), zap.String("pattern", handler.Pattern()))
			if e.metrics != nil {
				e.metrics.RecordInline(false, 0, time.Since(start), compileErr)
			}
			if svc != nil {
				fallback := fallbackErrorResults(compileErr)
				tgRes := e.serializeResults(fallback)
				_ = svc.AnswerInlineQueryOptions(ctx, queryID, tgRes, core.InlineAnswerOptions{NextOffset: "", CacheTime: 1, Private: true})
			}
			return compileErr
		}
		pageResults = compiledPage
		interactionSessions = created
		defer func() {
			if interactionSessionsCommitted || e.sessions == nil {
				return
			}
			for _, id := range interactionSessions {
				e.sessions.Cancel(id)
			}
		}()
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
		if ansErr == nil {
			interactionSessionsCommitted = true
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
