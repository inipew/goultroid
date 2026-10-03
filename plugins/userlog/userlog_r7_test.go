package userlog_test

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/plugins/userlog"
	"go.uber.org/zap"
)

type r7Ticket struct{ id tasks.TaskID }

func (t *r7Ticket) TaskID() tasks.TaskID                         { return t.id }
func (*r7Ticket) State() tasks.TaskState                         { return tasks.StateQueued }
func (*r7Ticket) Done() <-chan struct{}                          { return make(chan struct{}) }
func (*r7Ticket) Result() (tasks.TaskResult, bool)               { return tasks.TaskResult{}, false }
func (*r7Ticket) Wait(context.Context) (tasks.TaskResult, error) { return tasks.TaskResult{}, nil }

type r7RecordingTaskClient struct{ specs chan tasks.WorkSpec }

func (c *r7RecordingTaskClient) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs <- spec
	return &r7Ticket{id: spec.ID}, nil
}
func (*r7RecordingTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*r7RecordingTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*r7RecordingTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestR7UserLogMessageHookUsesSingleSharedEventTask(t *testing.T) {
	p := userlog.New(nil, 12345)
	registrations := p.MessageHookRegistrations()
	if len(registrations) != 1 {
		t.Fatalf("registrations=%d, want 1", len(registrations))
	}
	registration := registrations[0]
	if registration.Routing.Lane != core.MessageHookEvent {
		t.Fatalf("lane=%v, want event", registration.Routing.Lane)
	}
	if registration.Execution.FailurePolicy != core.MessageHookFailOpen {
		t.Fatalf("failure policy=%v, want fail-open", registration.Execution.FailurePolicy)
	}
	if registration.Execution.Ordering != core.MessageHookOrderingPlugin {
		t.Fatalf("ordering=%v, want plugin-global", registration.Execution.Ordering)
	}
	if registration.Execution.HandlerTimeout != 15*time.Second || registration.Execution.TaskTimeout != 15*time.Second {
		t.Fatalf("timeouts=%s/%s, want 15s/15s", registration.Execution.HandlerTimeout, registration.Execution.TaskTimeout)
	}
}

func TestR7AdminActionDemotesTelegramDeliveryToNormalSharedTask(t *testing.T) {
	db := setupTestDB(t)
	repo := userlogSvc.NewSQLiteRepository(db)
	mockTG := &mockTelegram{}
	svc := userlogSvc.NewService(repo, mockTG, zap.NewNop())
	if err := svc.SetLogChat(context.Background(), 777); err != nil {
		t.Fatal(err)
	}

	recorder := &r7RecordingTaskClient{specs: make(chan tasks.WorkSpec, 1)}
	scope := plugin.NewScope(context.Background(), "plugin:userlog")
	gate := plugin.NewCapabilityGate()
	gate.Register("userlog", []string{plugin.CapTasks})
	pctx := plugin.NewPluginContext(scope.Context(), plugin.ContextConfig{
		Scope:      scope,
		Owner:      "userlog",
		Gate:       gate,
		TaskClient: recorder,
	})

	p := userlog.New(svc, 12345)
	if err := p.InitPlugin(pctx); err != nil {
		t.Fatal(err)
	}
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	p.SetEventBus(bus)

	bus.Publish(&core.AdminActionEvent{
		At:       time.Now(),
		Action:   "ban",
		ActorID:  12345,
		TargetID: 999,
		Success:  true,
	})

	var spec tasks.WorkSpec
	select {
	case spec = <-recorder.specs:
	case <-time.After(time.Second):
		t.Fatal("admin action did not submit UserLog continuation")
	}
	if spec.Pool != tasks.PoolID("general") || spec.Class != tasks.PriorityNormal {
		t.Fatalf("UserLog continuation pool/class=%s/%s, want general/normal", spec.Pool, spec.Class)
	}
	if spec.OrderingKey != "msg-event:plugin:userlog" {
		t.Fatalf("ordering key=%q, want UserLog plugin-global domain", spec.OrderingKey)
	}
	if spec.ExecutionTimeout != 15*time.Second {
		t.Fatalf("execution timeout=%s, want 15s", spec.ExecutionTimeout)
	}
	if spec.Scope.Owner != "plugin:userlog" || spec.Scope.Generation != scope.Generation() {
		t.Fatalf("scope=%+v, want plugin:userlog generation %d", spec.Scope, scope.Generation())
	}
	if got := mockTG.getSent(); got != "" {
		t.Fatalf("AdminAction EventBus callback performed Telegram RPC before normal continuation: %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.ShutdownContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := scope.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestR7UserLogOwnsNoPrivateWorker(t *testing.T) {
	db := setupTestDB(t)
	repo := userlogSvc.NewSQLiteRepository(db)
	mockTG := &mockTelegram{}
	svc := userlogSvc.NewService(repo, mockTG, zap.NewNop())
	if err := svc.SetLogChat(context.Background(), 777); err != nil {
		t.Fatal(err)
	}

	p := userlog.New(svc, 12345)
	scope := plugin.NewScope(context.Background(), "plugin:userlog")
	if err := p.InitScope(scope.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if got := scope.ActiveGoroutines(); got != 0 {
		t.Fatalf("UserLog started %d private goroutine(s), want 0", got)
	}

	msg := &core.MessageEnvelope{
		ChatID:     999,
		Chat:       core.Chat{ID: 999, Type: "private"},
		Text:       "hello",
		Peer:       core.PeerRef{Kind: core.PeerKindUser, ID: 999},
		SenderPeer: core.PeerRef{Kind: core.PeerKindUser, ID: 999},
		Sender:     core.User{ID: 999, FirstName: "Alice"},
	}
	if err := p.HandleMessageEvent(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if got := scope.ActiveGoroutines(); got != 0 {
		t.Fatalf("UserLog retained %d private goroutine(s) after delivery, want 0", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.ShutdownContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := scope.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
