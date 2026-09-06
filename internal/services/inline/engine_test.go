package inline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
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
}

func (r *recordingInlineService) AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error {
	r.lastQueryID = queryID
	r.lastResults = results
	r.lastNextOffset = nextOffset
	r.lastCacheTime = cacheTime
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
