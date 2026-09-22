package inline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/ui"
	"go.uber.org/zap"
)

type mockInlineHandler struct {
	pattern     string
	description string
	results     []InlineResult
	err         error
	invoked     bool
	lastCtx     *InlineContext
}

func (m *mockInlineHandler) Pattern() string     { return m.pattern }
func (m *mockInlineHandler) Description() string { return m.description }
func (m *mockInlineHandler) HandleInline(ctx *InlineContext) ([]InlineResult, error) {
	m.invoked = true
	m.lastCtx = ctx
	return m.results, m.err
}

type recordingInlineService struct {
	core.MockTelegramServicer
	lastQueryID    int64
	lastResults    []tg.InputBotInlineResultClass
	lastNextOffset string
	lastCacheTime  int
	lastOpts       core.InlineAnswerOptions
}

func (r *recordingInlineService) AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error {
	r.lastQueryID = queryID
	r.lastResults = results
	r.lastNextOffset = nextOffset
	r.lastCacheTime = cacheTime
	r.lastOpts = core.InlineAnswerOptions{
		Results:    results,
		NextOffset: nextOffset,
		CacheTime:  cacheTime,
	}
	return nil
}

func (r *recordingInlineService) AnswerInlineQueryOptions(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, opts core.InlineAnswerOptions) error {
	r.lastQueryID = queryID
	if opts.Results != nil {
		r.lastResults = opts.Results
	} else {
		r.lastResults = results
	}
	r.lastNextOffset = opts.NextOffset
	r.lastCacheTime = opts.CacheTime
	r.lastOpts = opts
	return nil
}

func TestRegistry_RegisterAndResolve(t *testing.T) {
	reg := NewRegistry()

	hHelp := &mockInlineHandler{pattern: "help"}
	hPing := &mockInlineHandler{pattern: "ping"}
	hDefault := &mockInlineHandler{pattern: ""}

	if err := reg.Register(hHelp); err != nil {
		t.Fatalf("failed to register hHelp: %v", err)
	}
	if err := reg.Register(hPing); err != nil {
		t.Fatalf("failed to register hPing: %v", err)
	}
	if err := reg.Register(hDefault); err != nil {
		t.Fatalf("failed to register hDefault: %v", err)
	}

	// Duplicate
	if err := reg.Register(hHelp); err == nil {
		t.Errorf("expected error on duplicate register")
	}

	// Exact match
	h, args, ok := reg.Resolve("help command1 command2")
	if !ok || h != hHelp || len(args) != 2 || args[0] != "command1" || args[1] != "command2" {
		t.Errorf("unexpected resolve for help: ok=%v, args=%v", ok, args)
	}

	// Empty query matches default
	h, args, ok = reg.Resolve("")
	if !ok || h != hDefault || len(args) != 0 {
		t.Errorf("unexpected resolve for empty query: ok=%v, args=%v", ok, args)
	}

	// Unknown query falls back to default
	h, args, ok = reg.Resolve("unknown-kw foo")
	if !ok || h != hDefault || len(args) != 2 {
		t.Errorf("unexpected resolve for unknown query fallback: ok=%v, args=%v", ok, args)
	}
}

func TestRegistry_OwnedRegistrationAndStaleCleanup(t *testing.T) {
	reg := NewRegistry()
	scope1 := tasks.ScopeIdentity{Owner: "plugin:inline-test", Generation: 1}
	firstHandler := &mockInlineHandler{pattern: "owned"}
	firstRegistration, err := reg.RegisterOwned("inline-test", "lookup", scope1, firstHandler, 0)
	if err != nil {
		t.Fatalf("RegisterOwned(first) error = %v", err)
	}
	first, ok := reg.ResolveOwned("owned arg")
	if !ok {
		t.Fatal("owned handler did not resolve")
	}
	if first.FeatureID != "inline-test" || first.InteractionID != "lookup" || first.Scope != scope1 {
		t.Fatalf("unexpected resolved ownership: %+v", first)
	}
	oldToken := firstRegistration.token
	firstRegistration.Close()

	scope2 := tasks.ScopeIdentity{Owner: "plugin:inline-test", Generation: 2}
	secondHandler := &mockInlineHandler{pattern: "owned"}
	secondRegistration, err := reg.RegisterOwned("inline-test", "lookup", scope2, secondHandler, 0)
	if err != nil {
		t.Fatalf("RegisterOwned(second) error = %v", err)
	}
	defer secondRegistration.Close()

	staleCleanup := &Registration{registry: reg, pattern: "owned", token: oldToken}
	staleCleanup.Close()
	second, ok := reg.ResolveOwned("owned")
	if !ok || second.Handler != secondHandler || second.Scope != scope2 {
		t.Fatalf("stale cleanup removed replacement: ok=%v resolved=%+v", ok, second)
	}
}

func TestEngine_PreparedOwnedQueryRejectsStaleGeneration(t *testing.T) {
	reg := NewRegistry()
	handler := &mockInlineHandler{
		pattern: "prepared",
		results: []InlineResult{{ID: "ok", Title: "OK", Text: "OK"}},
	}
	scope := tasks.ScopeIdentity{Owner: "plugin:prepared", Generation: 7}
	registration, err := reg.RegisterOwned("prepared", "lookup", scope, handler, 0)
	if err != nil {
		t.Fatalf("RegisterOwned() error = %v", err)
	}
	engine := NewEngine(reg, zap.NewNop())
	prepared, err := engine.Prepare("prepared value")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Scope() != scope {
		t.Fatalf("prepared scope = %+v, want %+v", prepared.Scope(), scope)
	}
	registration.Close()

	err = engine.ExecutePreparedWithPeerType(
		context.Background(),
		&recordingInlineService{},
		777,
		123,
		prepared,
		"",
		nil,
	)
	if !errors.Is(err, ErrStaleHandler) {
		t.Fatalf("ExecutePreparedWithPeerType() error = %v, want %v", err, ErrStaleHandler)
	}
	if handler.invoked {
		t.Fatal("stale prepared handler executed after registration removal")
	}
}

func TestEngine_TypedFeatureActionsCompileToA2AndClaimInlineTarget(t *testing.T) {
	catalog := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:inline-actions", Generation: 1}
	featureRegistration, err := catalog.Register(feature.Owner{ID: "inline-actions", Scope: scope}, feature.Spec{
		ID:   "inline-actions",
		Name: "Inline Actions",
		Interactions: []feature.Interaction{
			{
				ID: "lookup", Kind: feature.InteractionInline,
				Surfaces: execution.SurfaceInline,
				Policy:   feature.PublicPolicy(execution.SurfaceInline),
			},
			{
				ID: "choose", Kind: feature.InteractionAction,
				Surfaces: execution.SurfaceInline,
				Policy:   feature.PublicPolicy(execution.SurfaceInline),
			},
		},
	})
	if err != nil {
		t.Fatalf("feature register error = %v", err)
	}
	defer featureRegistration.Close()

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer sessions.Close()

	registry := NewRegistry()
	handler := &mockInlineHandler{
		pattern: "typed",
		results: []InlineResult{{
			ID:               "typed-1",
			Title:            "Typed action",
			Text:             "Choose this result",
			InteractionState: []byte("result:1"),
			InteractionTTL:   5 * time.Minute,
			ActionRows: []presentation.Row{{
				{Text: "Choose", ActionID: "choose"},
			}},
		}},
	}
	if _, err := registry.RegisterOwned("inline-actions", "lookup", scope, handler, 0); err != nil {
		t.Fatalf("RegisterOwned() error = %v", err)
	}

	engine := NewEngine(registry, zap.NewNop())
	engine.SetFeatureCatalog(catalog)
	engine.SetInteractionRuntime(sessions)
	prepared, err := engine.Prepare("typed")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	svc := &recordingInlineService{}
	if err := engine.ExecutePreparedWithPeerType(context.Background(), svc, 7001, 42, prepared, "", nil); err != nil {
		t.Fatalf("ExecutePreparedWithPeerType() error = %v", err)
	}
	if !svc.lastOpts.Private || svc.lastCacheTime != 0 {
		t.Fatalf("interactive inline answer must be private/no-cache: %+v", svc.lastOpts)
	}
	if engine.Cache().Len() != 0 {
		t.Fatalf("interactive result populated local cache: %d entries", engine.Cache().Len())
	}
	if stats := sessions.Stats(); stats.Sessions != 1 {
		t.Fatalf("interaction sessions=%d, want exactly emitted result session", stats.Sessions)
	}
	if len(svc.lastResults) != 1 {
		t.Fatalf("telegram results=%d, want 1", len(svc.lastResults))
	}
	item, ok := svc.lastResults[0].(*tg.InputBotInlineResult)
	if !ok {
		t.Fatalf("telegram result type = %T", svc.lastResults[0])
	}
	message, ok := item.SendMessage.(*tg.InputBotInlineMessageText)
	if !ok {
		t.Fatalf("send message type = %T", item.SendMessage)
	}
	markup, ok := message.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !ok || len(markup.Rows) != 1 || len(markup.Rows[0].Buttons) != 1 {
		t.Fatalf("compiled markup = %#v", message.ReplyMarkup)
	}
	button, ok := markup.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok {
		t.Fatalf("compiled button type = %T", markup.Rows[0].Buttons[0])
	}
	token, err := rootinteraction.ParseCallbackToken(button.Data)
	if err != nil {
		t.Fatalf("compiled callback is not a2: %q: %v", button.Data, err)
	}
	resolved, err := sessions.ResolveCallback(context.Background(), button.Data, rootinteraction.Binding{
		ActorID:         42,
		InlineMessageID: "inline:one",
	})
	if err != nil {
		t.Fatalf("ResolveCallback(first inline target) error = %v", err)
	}
	if resolved.Session.ID != token.SessionID || string(resolved.Session.State) != "result:1" {
		t.Fatalf("resolved typed inline state mismatch: token=%+v session=%+v", token, resolved.Session)
	}
	if _, err := sessions.ResolveCallback(context.Background(), button.Data, rootinteraction.Binding{
		ActorID:         42,
		InlineMessageID: "inline:copy",
	}); !errors.Is(err, rootinteraction.ErrBindingMismatch) {
		t.Fatalf("copied inline target error = %v, want binding mismatch", err)
	}
}

func TestEngine_FeatureOwnedInlineRejectsRawCallbackData(t *testing.T) {
	catalog := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:inline-raw", Generation: 1}
	featureRegistration, err := catalog.Register(feature.Owner{ID: "inline-raw", Scope: scope}, feature.Spec{
		ID:   "inline-raw",
		Name: "Inline Raw",
		Interactions: []feature.Interaction{{
			ID: "lookup", Kind: feature.InteractionInline,
			Surfaces: execution.SurfaceInline,
			Policy:   feature.PublicPolicy(execution.SurfaceInline),
		}},
	})
	if err != nil {
		t.Fatalf("feature register error = %v", err)
	}
	defer featureRegistration.Close()

	registry := NewRegistry()
	markup := ui.NewMarkup(ui.ButtonRow{ui.NewCallbackButton("Legacy", []byte("raw:callback"))})
	handler := &mockInlineHandler{
		pattern: "raw",
		results: []InlineResult{{ID: "raw", Title: "Raw", Text: "Raw", Markup: &markup}},
	}
	if _, err := registry.RegisterOwned("inline-raw", "lookup", scope, handler, 0); err != nil {
		t.Fatalf("RegisterOwned() error = %v", err)
	}
	engine := NewEngine(registry, zap.NewNop())
	prepared, err := engine.Prepare("raw")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	err = engine.ExecutePreparedWithPeerType(context.Background(), &recordingInlineService{}, 7002, 42, prepared, "", nil)
	if err == nil || !strings.Contains(err.Error(), "raw callback") {
		t.Fatalf("feature-owned raw callback error = %v", err)
	}
}

func TestPaginator(t *testing.T) {
	paginator := NewPaginator(3) // 3 items per page

	var items []InlineResult
	for i := 0; i < 7; i++ {
		items = append(items, InlineResult{ID: fmt.Sprintf("%d", i)})
	}

	// Page 1 (offset "")
	p1, next1 := paginator.Paginate(items, "")
	if len(p1) != 3 || next1 != "3" || p1[0].ID != "0" || p1[2].ID != "2" {
		t.Errorf("unexpected page 1: len=%d, next=%s", len(p1), next1)
	}

	// Page 2 (offset "3")
	p2, next2 := paginator.Paginate(items, "3")
	if len(p2) != 3 || next2 != "6" || p2[0].ID != "3" || p2[2].ID != "5" {
		t.Errorf("unexpected page 2: len=%d, next=%s", len(p2), next2)
	}

	// Page 3 (offset "6") -> only 1 item left
	p3, next3 := paginator.Paginate(items, "6")
	if len(p3) != 1 || next3 != "" || p3[0].ID != "6" {
		t.Errorf("unexpected page 3: len=%d, next=%s", len(p3), next3)
	}

	// Out of bounds (offset "10")
	p4, next4 := paginator.Paginate(items, "10")
	if len(p4) != 0 || next4 != "" {
		t.Errorf("unexpected page 4: len=%d, next=%s", len(p4), next4)
	}
}

func TestCache(t *testing.T) {
	cache := NewCache(50 * time.Millisecond)

	cache.Set("test", []InlineResult{{ID: "1"}}, 50*time.Millisecond)
	res, ok := cache.Get("test")
	if !ok || len(res) != 1 {
		t.Fatalf("expected cached results")
	}

	time.Sleep(70 * time.Millisecond)
	_, ok = cache.Get("test")
	if ok {
		t.Errorf("expected cache entry to be expired")
	}

	cache.Set("prune", []InlineResult{{ID: "2"}}, 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	pruned := cache.Prune()
	if pruned != 1 {
		t.Errorf("expected 1 pruned entry, got %d", pruned)
	}
}

func TestEngine_Execute(t *testing.T) {
	reg := NewRegistry()
	markup := ui.NewMarkup(ui.ButtonRow{ui.NewURLButton("Doc", "https://docs.org")})
	handler := &mockInlineHandler{
		pattern: "status",
		results: []InlineResult{
			{
				ID:          "stat-1",
				Title:       "Userbot Status",
				Description: "Online and active",
				Text:        "Status: <b>Healthy</b>",
				Markup:      &markup,
				ThumbURL:    "https://example.com/thumb.jpg",
			},
		},
	}
	_ = reg.Register(handler)

	engine := NewEngine(reg, zap.NewNop())
	svc := &recordingInlineService{}

	ctx := context.Background()
	err := engine.Execute(ctx, svc, 12345, 9999, "status arg1", "")
	if err != nil {
		t.Fatalf("unexpected engine execution error: %v", err)
	}

	if !handler.invoked {
		t.Fatalf("expected handler to be invoked")
	}
	if len(handler.lastCtx.Args) != 1 || handler.lastCtx.Args[0] != "arg1" {
		t.Errorf("unexpected args: %+v", handler.lastCtx.Args)
	}

	if svc.lastQueryID != 12345 {
		t.Errorf("expected query ID 12345, got %d", svc.lastQueryID)
	}
	if len(svc.lastResults) != 1 {
		t.Fatalf("expected 1 MTProto result, got %d", len(svc.lastResults))
	}

	res0, ok := svc.lastResults[0].(*tg.InputBotInlineResult)
	if !ok {
		t.Fatalf("expected *tg.InputBotInlineResult, got %T", svc.lastResults[0])
	}
	if res0.ID != "stat-1" || res0.Title != "Userbot Status" {
		t.Errorf("unexpected MTProto result: %+v", res0)
	}

	// Second execution should hit cache
	handler.invoked = false
	err = engine.Execute(ctx, svc, 12346, 9999, "status arg1", "")
	if err != nil {
		t.Fatalf("cache hit execution error: %v", err)
	}
	if handler.invoked {
		t.Errorf("handler should not be invoked when results are cached")
	}

	// No matching handler
	err = engine.Execute(ctx, svc, 99999, 9999, "nonexistent", "")
	if !errors.Is(err, ErrNoMatchingHandler) {
		t.Errorf("expected ErrNoMatchingHandler, got %v", err)
	}
}

type mockInlineHandlerV2 struct {
	mockInlineHandler
	matcher      InlineMatcher
	accessPolicy InlineAccessPolicy
	cachePolicy  CachePolicy
	response     *InlineResponse
}

func (m *mockInlineHandlerV2) Matcher() InlineMatcher           { return m.matcher }
func (m *mockInlineHandlerV2) AccessPolicy() InlineAccessPolicy { return m.accessPolicy }
func (m *mockInlineHandlerV2) CachePolicy() CachePolicy         { return m.cachePolicy }
func (m *mockInlineHandlerV2) HandleInlineV2(ctx *InlineContext) (*InlineResponse, error) {
	m.invoked = true
	m.lastCtx = ctx
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func TestEngine_Matcher_PrefixAndRegex(t *testing.T) {
	reg := NewRegistry()

	prefixMatcher := NewPrefixMatcher("wiki")
	hPrefix := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "wiki"},
		matcher:           prefixMatcher,
		response: &InlineResponse{
			Results: []InlineResult{{ID: "wiki-1", Title: "Wikipedia"}},
		},
	}
	_ = reg.RegisterWithPriority(hPrefix, 10)

	regexMatcher, _ := NewRegexMatcher(`^math\s+(\d+)\+(\d+)`)
	hRegex := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "math"},
		matcher:           regexMatcher,
		response: &InlineResponse{
			Results: []InlineResult{{ID: "math-1", Title: "Calculator"}},
		},
	}
	_ = reg.RegisterWithPriority(hRegex, 20)

	// Test prefix match
	h, args, ok := reg.Resolve("wiki golang programming")
	if !ok || h != hPrefix || len(args) != 2 || args[0] != "golang" || args[1] != "programming" {
		t.Errorf("unexpected prefix resolve: ok=%v h=%v args=%v", ok, h, args)
	}

	// Test regex match
	h, args, ok = reg.Resolve("math 5+10")
	if !ok || h != hRegex || len(args) != 2 || args[0] != "5" || args[1] != "10" {
		t.Errorf("unexpected regex resolve: ok=%v h=%v args=%v", ok, h, args)
	}
}

func TestEngine_AccessPolicy(t *testing.T) {
	reg := NewRegistry()
	hOwner := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "secret"},
		accessPolicy:      InlineAccessPolicy{OwnerOnly: true},
		response: &InlineResponse{
			Results: []InlineResult{{ID: "s-1", Title: "Secret"}},
		},
	}
	_ = reg.Register(hOwner)

	perms := core.NewPermissions(1111, []int64{2222}) // owner=1111, sudo=2222
	engine := NewEngine(reg, zap.NewNop())
	engine.SetPermissions(perms)
	svc := &recordingInlineService{}
	ctx := context.Background()

	// 1. Non-owner (3333) should be rejected
	err := engine.Execute(ctx, svc, 1, 3333, "secret", "")
	if err == nil {
		t.Fatalf("expected error for unauthorized user, got nil")
	}
	if hOwner.invoked {
		t.Errorf("handler should not have been invoked for non-owner")
	}

	// 2. Owner (1111) should succeed
	hOwner.invoked = false
	err = engine.Execute(ctx, svc, 2, 1111, "secret", "")
	if err != nil {
		t.Fatalf("unexpected error for owner: %v", err)
	}
	if !hOwner.invoked {
		t.Errorf("handler should have been invoked for owner")
	}
}

func TestEngine_NativePagination(t *testing.T) {
	reg := NewRegistry()
	hPaging := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "paged"},
		response: &InlineResponse{
			Results:    []InlineResult{{ID: "item-1"}, {ID: "item-2"}},
			NextOffset: "offset-token-abc",
		},
	}
	_ = reg.Register(hPaging)

	engine := NewEngine(reg, zap.NewNop())
	svc := &recordingInlineService{}
	ctx := context.Background()

	err := engine.Execute(ctx, svc, 10, 100, "paged", "")
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}
	if svc.lastNextOffset != "offset-token-abc" {
		t.Errorf("expected native next offset 'offset-token-abc', got %q", svc.lastNextOffset)
	}
	if len(svc.lastResults) != 2 {
		t.Errorf("expected 2 results, got %d", len(svc.lastResults))
	}
}

type panickingInlineHandler struct{}

func (p *panickingInlineHandler) Pattern() string     { return "inlinepanic" }
func (p *panickingInlineHandler) Description() string { return "panics" }
func (p *panickingInlineHandler) HandleInline(ctx *InlineContext) ([]InlineResult, error) {
	panic("inline test panic")
}

func TestEngine_PanicRecovery(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(&panickingInlineHandler{})

	engine := NewEngine(reg, zap.NewNop())
	svc := &recordingInlineService{}
	ctx := context.Background()

	err := engine.Execute(ctx, svc, 99, 100, "inlinepanic", "")
	if err == nil {
		t.Fatalf("expected error from recovered panic, got nil")
	}
	if !errors.Is(err, core.ErrInternal) {
		t.Errorf("expected ErrInternal from panic recovery, got %v", err)
	}
}

func TestEngine_AccessPolicy_AllowedChats_FailClosed(t *testing.T) {
	reg := NewRegistry()
	hChatSpecific := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "groupquery"},
		accessPolicy:      InlineAccessPolicy{AllowedChats: []int64{12345}},
		response: &InlineResponse{
			Results: []InlineResult{{ID: "g-1", Title: "Group Only"}},
		},
	}
	_ = reg.Register(hChatSpecific)

	perms := core.NewPermissions(1111, nil)
	engine := NewEngine(reg, zap.NewNop())
	engine.SetPermissions(perms)
	svc := &recordingInlineService{}
	ctx := context.Background()

	// Even owner (1111) must be rejected because AllowedChats is fail-closed in inline mode
	err := engine.ExecuteWithPeerType(ctx, svc, 1, 1111, "groupquery", "", &tg.InlineQueryPeerTypeChat{})
	if err == nil {
		t.Fatalf("expected error for AllowedChats fail-closed policy, got nil")
	}
	if hChatSpecific.invoked {
		t.Errorf("handler with AllowedChats must never be invoked in inline mode")
	}
}

func TestEngine_AccessPolicy_AllowedChatTypes(t *testing.T) {
	reg := NewRegistry()
	hPMOnly := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "pmquery"},
		accessPolicy:      InlineAccessPolicy{AllowedChatTypes: []InlineChatType{ChatTypePrivate}},
		response: &InlineResponse{
			Results: []InlineResult{{ID: "pm-1", Title: "PM Only"}},
		},
	}
	_ = reg.Register(hPMOnly)

	engine := NewEngine(reg, zap.NewNop())
	svc := &recordingInlineService{}
	ctx := context.Background()

	// 1. Private chat peer type should be allowed
	hPMOnly.invoked = false
	err := engine.ExecuteWithPeerType(ctx, svc, 101, 200, "pmquery", "", &tg.InlineQueryPeerTypePM{})
	if err != nil {
		t.Fatalf("unexpected error for matching ChatTypePrivate: %v", err)
	}
	if !hPMOnly.invoked {
		t.Fatalf("expected handler to be invoked for matching ChatTypePrivate")
	}

	// 2. Group peer type should be rejected
	hPMOnly.invoked = false
	err = engine.ExecuteWithPeerType(ctx, svc, 102, 200, "pmquery", "", &tg.InlineQueryPeerTypeChat{})
	if err == nil {
		t.Fatalf("expected error for mismatched ChatTypeChat, got nil")
	}
	if hPMOnly.invoked {
		t.Fatalf("handler should not be invoked for mismatched ChatType")
	}

	// 3. Nil peer type should be rejected (cannot verify)
	hPMOnly.invoked = false
	err = engine.ExecuteWithPeerType(ctx, svc, 103, 200, "pmquery", "", nil)
	if err == nil {
		t.Fatalf("expected error for nil peer type, got nil")
	}
	if hPMOnly.invoked {
		t.Fatalf("handler should not be invoked for nil peer type")
	}
}

func TestEngine_Cache_CallbackButtons_AutoDowngradeToPerUser(t *testing.T) {
	reg := NewRegistry()
	btnMarkup := ui.NewMarkup(
		ui.ButtonRow{
			ui.NewCallbackButton("Action", []byte("state_payload_123")),
		},
	)
	// Handler with CacheGlobal, but returns results containing a callback button
	hInteractive := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "interactive"},
		cachePolicy:       CacheGlobal,
		response: &InlineResponse{
			Results: []InlineResult{
				{
					ID:     "btn-1",
					Title:  "Click Me",
					Markup: &btnMarkup,
				},
			},
		},
	}
	_ = reg.Register(hInteractive)

	engine := NewEngine(reg, zap.NewNop())
	svc := &recordingInlineService{}
	ctx := context.Background()

	// First query by User 1
	hInteractive.invoked = false
	err := engine.Execute(ctx, svc, 201, 1001, "interactive", "")
	if err != nil {
		t.Fatalf("user 1 query failed: %v", err)
	}
	if !hInteractive.invoked {
		t.Fatalf("expected handler to be invoked for user 1")
	}

	// Second query by User 2 with same query text
	// Because results had callback buttons, CacheGlobal should have been auto-downgraded to CachePerUser,
	// so User 2 must NOT hit User 1's cache!
	hInteractive.invoked = false
	err = engine.Execute(ctx, svc, 202, 1002, "interactive", "")
	if err != nil {
		t.Fatalf("user 2 query failed: %v", err)
	}
	if !hInteractive.invoked {
		t.Fatalf("expected handler to be invoked for user 2 due to auto-downgraded CachePerUser")
	}

	// User 1 querying again SHOULD hit their own cache
	hInteractive.invoked = false
	err = engine.Execute(ctx, svc, 203, 1001, "interactive", "")
	if err != nil {
		t.Fatalf("user 1 second query failed: %v", err)
	}
	if hInteractive.invoked {
		t.Fatalf("expected user 1 second query to hit their own per-user cache")
	}
}

func TestEngine_HardCap_Max50Results(t *testing.T) {
	reg := NewRegistry()
	var sixtyResults []InlineResult
	for i := 0; i < 60; i++ {
		sixtyResults = append(sixtyResults, InlineResult{
			ID:    fmt.Sprintf("item-%d", i),
			Title: fmt.Sprintf("Result %d", i),
		})
	}

	hMany := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "many"},
		cachePolicy:       CacheGlobal,
		response: &InlineResponse{
			Results: sixtyResults,
		},
	}
	_ = reg.Register(hMany)

	engine := NewEngine(reg, zap.NewNop())
	engine.SetPaginator(NewPaginator(50))
	svc := &recordingInlineService{}
	ctx := context.Background()

	// Initial execution: 60 results should be truncated to 50
	err := engine.Execute(ctx, svc, 301, 5001, "many", "")
	if err != nil {
		t.Fatalf("engine execution failed: %v", err)
	}
	if len(svc.lastResults) != 50 {
		t.Fatalf("expected exactly 50 results (hard capped), got %d", len(svc.lastResults))
	}

	// Subsequent execution (from cache)
	svc.lastResults = nil
	err = engine.Execute(ctx, svc, 302, 5001, "many", "")
	if err != nil {
		t.Fatalf("cache execution failed: %v", err)
	}
	if len(svc.lastResults) != 50 {
		t.Fatalf("expected exactly 50 results from cache (hard capped), got %d", len(svc.lastResults))
	}
}

func TestEngine_Response_SwitchPM_And_SwitchWebView(t *testing.T) {
	reg := NewRegistry()
	hSwitch := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "switchtest"},
		response: &InlineResponse{
			Results: []InlineResult{
				{ID: "sw-1", Title: "Switch Item", Text: "Hello"},
			},
			SwitchPM: &SwitchPM{
				Text:  "Connect Bot in PM",
				Query: "auth_token_xyz",
			},
			SwitchWebView: &SwitchWebView{
				Text: "Launch Mini App",
				URL:  "https://webapp.example.com",
			},
		},
	}
	_ = reg.Register(hSwitch)

	engine := NewEngine(reg, zap.NewNop())
	svc := &recordingInlineService{}
	ctx := context.Background()

	err := engine.Execute(ctx, svc, 401, 7001, "switchtest", "")
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if svc.lastOpts.SwitchPM == nil {
		t.Fatalf("expected SwitchPM to be populated in options")
	}
	if svc.lastOpts.SwitchPM.Text != "Connect Bot in PM" || svc.lastOpts.SwitchPM.StartParam != "auth_token_xyz" {
		t.Errorf("unexpected SwitchPM values: %+v", svc.lastOpts.SwitchPM)
	}

	if svc.lastOpts.SwitchWebView == nil {
		t.Fatalf("expected SwitchWebView to be populated in options")
	}
	if svc.lastOpts.SwitchWebView.Text != "Launch Mini App" || svc.lastOpts.SwitchWebView.URL != "https://webapp.example.com" {
		t.Errorf("unexpected SwitchWebView values: %+v", svc.lastOpts.SwitchWebView)
	}
}

func TestEngine_Response_GalleryAndPrivate(t *testing.T) {
	reg := NewRegistry()
	hGallery := &mockInlineHandlerV2{
		mockInlineHandler: mockInlineHandler{pattern: "gallerytest"},
		response: &InlineResponse{
			Results: []InlineResult{
				{ID: "gal-1", Title: "Photo 1", MediaURL: "https://example.com/1.jpg", Type: ResultPhoto},
				{ID: "gal-2", Title: "Photo 2", MediaURL: "https://example.com/2.jpg", Type: ResultPhoto},
			},
			Gallery:   true,
			Private:   true,
			CacheTime: 60, // should be forced to 0 because Private is true
		},
	}
	_ = reg.Register(hGallery)

	engine := NewEngine(reg, zap.NewNop())
	svc := &recordingInlineService{}
	ctx := context.Background()

	err := engine.Execute(ctx, svc, 501, 8001, "gallerytest", "")
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !svc.lastOpts.Gallery {
		t.Errorf("expected Gallery=true in answer options")
	}
	if !svc.lastOpts.Private {
		t.Errorf("expected Private=true in answer options")
	}
	if svc.lastOpts.CacheTime != 0 {
		t.Errorf("expected CacheTime=0 for private results, got %d", svc.lastOpts.CacheTime)
	}
}
