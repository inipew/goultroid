package client_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/client"
	"github.com/inipew/goultroid/internal/core"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

func TestLifecycle(t *testing.T) {
	lc := client.NewLifecycle()
	if lc.State() != client.StateNew {
		t.Fatalf("expected StateNew, got %v", lc.State())
	}

	if !lc.TryStart() {
		t.Fatalf("expected TryStart to succeed from StateNew")
	}
	if lc.State() != client.StateStarting {
		t.Fatalf("expected StateStarting, got %v", lc.State())
	}

	// Double start should fail
	if lc.TryStart() {
		t.Fatalf("expected second TryStart to fail")
	}

	lc.SetState(client.StateRunning)
	if lc.State() != client.StateRunning {
		t.Fatalf("expected StateRunning, got %v", lc.State())
	}

	if !lc.TryStop() {
		t.Fatalf("expected TryStop to succeed from StateRunning")
	}
	if lc.State() != client.StateStopping {
		t.Fatalf("expected StateStopping, got %v", lc.State())
	}

	lc.SetState(client.StateStopped)
	if lc.State() != client.StateStopped {
		t.Fatalf("expected StateStopped, got %v", lc.State())
	}

	// Can start again after stopped
	if !lc.TryStart() {
		t.Fatalf("expected TryStart to succeed from StateStopped")
	}

	lc.SetState(client.StateStopping)
	if lc.TryStart() {
		t.Fatal("expected TryStart to reject StateStopping")
	}
	lc.SetState(client.StateFailed)
	if !lc.TryStart() {
		t.Fatal("expected TryStart to recover from StateFailed")
	}
	lc.SetState(client.StateFailed)
	if lc.TryStop() {
		t.Fatal("expected TryStop to reject StateFailed")
	}
}

func TestUserRateLimiter(t *testing.T) {
	limiter := client.NewUserRateLimiter(3, 50*time.Millisecond)

	// User 1 consumes 3 tokens
	if !limiter.Allow(1, "command") {
		t.Fatalf("expected token 1 to be allowed")
	}
	if !limiter.Allow(1, "command") {
		t.Fatalf("expected token 2 to be allowed")
	}
	if !limiter.Allow(1, "command") {
		t.Fatalf("expected token 3 to be allowed")
	}

	// 4th token should be blocked
	if limiter.Allow(1, "command") {
		t.Fatalf("expected 4th token to be blocked")
	}

	// User 2 on another category should have fresh bucket
	if !limiter.Allow(2, "command") {
		t.Fatalf("expected user 2 to be allowed")
	}

	// After refill interval, token should be available
	time.Sleep(60 * time.Millisecond)
	if !limiter.Allow(1, "command") {
		t.Fatalf("expected refilled token to be allowed")
	}
}

func TestUserRateLimiter_Concurrent(t *testing.T) {
	limiter := client.NewUserRateLimiter(10, time.Second)

	var wg sync.WaitGroup
	var allowedCount int32
	var mu sync.Mutex

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Allow(999, "callback") {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowedCount != 10 {
		t.Fatalf("expected exactly 10 requests allowed, got %d", allowedCount)
	}
}

func TestUpdateHandlers_ShutdownBarrier(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	processed := false

	// CacheEntities is the first observable side effect after the shutdown
	// admission barrier. If the barrier is bypassed this callback becomes true.
	deps := client.UpdateHandlerDeps{
		IsShuttingDown: func() bool { return true },
		CacheEntities: func(tg.Entities) {
			processed = true
		},
	}
	client.RegisterUpdateHandlers(&dispatcher, deps)

	update := &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      123,
			Message: "/start",
			PeerID:  &tg.PeerUser{UserID: 42},
		},
	}

	if err := dispatcher.Handle(context.Background(), &tg.Updates{
		Updates: []tg.UpdateClass{update},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if processed {
		t.Fatalf("expected update to be rejected before any handler-side effect")
	}
}

type inlineAdmissionDynamicSource struct {
	scope tasks.ScopeIdentity
}

func (s *inlineAdmissionDynamicSource) Prepare(context.Context, string) (inlineservice.DynamicPrepared, bool, error) {
	return inlineservice.DynamicPrepared{
		Pattern: "saved",
		Scope:   s.scope,
		Resources: []tasks.ResourceRequirement{
			{Name: "media", Amount: 1},
		},
		State: "saved",
	}, true, nil
}

func (*inlineAdmissionDynamicSource) Execute(context.Context, inlineservice.DynamicPrepared, *inlineservice.InlineContext) (*inlineservice.InlineResponse, error) {
	return &inlineservice.InlineResponse{}, nil
}

type captureInlineTaskClient struct {
	spec  tasks.WorkSpec
	calls int
	run   bool
}

func (c *captureInlineTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.spec = spec
	c.calls++
	if c.run && spec.Handler != nil {
		if err := spec.Handler(ctx); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

type recordingAudienceRegistry struct {
	touches []pmrelay.AudienceTouch
}

func (r *recordingAudienceRegistry) TouchAudience(_ context.Context, touch pmrelay.AudienceTouch) (pmrelay.AudienceMember, error) {
	r.touches = append(r.touches, touch)
	return pmrelay.AudienceMember{
		UserID: touch.UserID, Sources: touch.Source,
		FirstSeenAt: touch.SeenAt, LastSeenAt: touch.SeenAt,
	}, nil
}
func (*recordingAudienceRegistry) SnapshotAudience(context.Context) (pmrelay.AudienceSnapshot, error) {
	return pmrelay.AudienceSnapshot{}, nil
}
func (*recordingAudienceRegistry) ListAudienceSnapshot(context.Context, pmrelay.AudienceSnapshot, int64, int) ([]pmrelay.AudienceMember, int64, error) {
	return nil, 0, nil
}
func (*captureInlineTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*captureInlineTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*captureInlineTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestUpdateHandlers_DynamicInlineMediaCarriesAdmissionAuthority(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	registry := inlineservice.NewRegistry()
	engine := inlineservice.NewEngine(registry, zap.NewNop())
	scope := tasks.ScopeIdentity{Owner: "plugin:saved", Generation: 11}
	engine.SetDynamicSource(&inlineAdmissionDynamicSource{scope: scope})

	taskClient := &captureInlineTaskClient{run: true}
	audience := &recordingAudienceRegistry{}
	client.RegisterUpdateHandlers(&dispatcher, client.UpdateHandlerDeps{
		InlineEngine:       engine,
		InlineService:      &core.MockTelegramServicer{},
		Tasks:              taskClient,
		AudienceRegistry:   audience,
	})

	update := &tg.UpdateBotInlineQuery{
		QueryID: 901,
		UserID:  42,
		Query:   "saved",
	}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{
		Updates: []tg.UpdateClass{update},
	}); err != nil {
		t.Fatalf("inline update error: %v", err)
	}
	if taskClient.calls != 1 {
		t.Fatalf("TaskEngine submissions=%d, want 1", taskClient.calls)
	}
	if taskClient.spec.Scope != scope {
		t.Fatalf("task scope=%+v, want %+v", taskClient.spec.Scope, scope)
	}
	if taskClient.spec.Pool != tasks.PoolID("general") ||
		taskClient.spec.Class != tasks.PriorityInteractive {
		t.Fatalf("task admission pool=%q class=%q", taskClient.spec.Pool, taskClient.spec.Class)
	}
	if taskClient.spec.ExecutionTimeout != 30*time.Second {
		t.Fatalf("media execution timeout=%s, want 30s", taskClient.spec.ExecutionTimeout)
	}
	if len(taskClient.spec.Resources) != 1 ||
		taskClient.spec.Resources[0].Name != "media" ||
		taskClient.spec.Resources[0].Amount != 1 {
		t.Fatalf("task resources=%+v, want media:1", taskClient.spec.Resources)
	}
	if len(audience.touches) != 1 {
		t.Fatalf("inline audience touches=%d, want 1", len(audience.touches))
	}
	if audience.touches[0].UserID != 42 || audience.touches[0].Source != pmrelay.AudienceSourceInline {
		t.Fatalf("inline audience touch=%+v", audience.touches[0])
	}
}


type failingInlineExecutor struct{}

func (failingInlineExecutor) Prepare(string) (inlineservice.PreparedQuery, error) {
	return inlineservice.PreparedQuery{}, inlineservice.ErrNoMatchingHandler
}

func (failingInlineExecutor) ExecuteWithPeerType(
	context.Context,
	core.TelegramServicer,
	int64,
	int64,
	string,
	string,
	tg.InlineQueryPeerTypeClass,
) error {
	return errors.New("inline execution failed")
}

func (failingInlineExecutor) ExecutePreparedWithPeerType(
	context.Context,
	core.TelegramServicer,
	int64,
	int64,
	inlineservice.PreparedQuery,
	string,
	tg.InlineQueryPeerTypeClass,
) error {
	return errors.New("prepared inline execution failed")
}

func TestUpdateHandlers_FailedInlineDoesNotTouchAudience(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	taskClient := &captureInlineTaskClient{run: true}
	audience := &recordingAudienceRegistry{}
	client.RegisterUpdateHandlers(&dispatcher, client.UpdateHandlerDeps{
		InlineEngine:     failingInlineExecutor{},
		InlineService:    &core.MockTelegramServicer{},
		Tasks:            taskClient,
		AudienceRegistry: audience,
	})

	update := &tg.UpdateBotInlineQuery{
		QueryID: 902,
		UserID:  42,
		Query:   "will-fail",
	}
	if err := dispatcher.Handle(context.Background(), &tg.Updates{
		Updates: []tg.UpdateClass{update},
	}); err != nil {
		t.Fatalf("inline update error=%v", err)
	}
	if len(audience.touches) != 0 {
		t.Fatalf("failed inline execution touched audience: %+v", audience.touches)
	}
}
