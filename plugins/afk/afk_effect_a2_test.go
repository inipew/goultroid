package afk

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/tasks"
)

// This test-only task client records an admitted task without running it until
// requested. It makes the decision/effect boundary independently observable.
type afkEffectTestClient struct {
	mu     sync.Mutex
	specs  []tasks.WorkSpec
	inline bool
	reject error
}

func (c *afkEffectTestClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	if c.reject != nil {
		return nil, c.reject
	}
	c.mu.Lock()
	c.specs = append(c.specs, spec)
	c.mu.Unlock()
	if c.inline && spec.Handler != nil {
		_ = spec.Handler(ctx)
	}
	return nil, nil
}
func (*afkEffectTestClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*afkEffectTestClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*afkEffectTestClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}
func (c *afkEffectTestClient) snapshot() []tasks.WorkSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]tasks.WorkSpec(nil), c.specs...)
}

func TestA2AFKWelcomeEffectIndependentOfDecisionBarrier(t *testing.T) {
	svc := &blockingWelcomeService{started: make(chan struct{}), release: make(chan struct{})}
	p := New(nil, 1001, func() core.TelegramServicer { return svc })
	client := &afkEffectTestClient{}
	p.SetTaskClient(client)
	scope := plugin.NewScope(context.Background(), "plugin:afk")
	if err := p.InitScope(scope.Context(), scope); err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case <-svc.release:
		default:
			close(svc.release)
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = scope.Close(closeCtx)
	}()
	if err := p.enableAFK(context.Background(), "meeting"); err != nil {
		t.Fatal(err)
	}

	input := &tg.Message{ID: 53, Out: true, PeerID: &tg.PeerUser{UserID: 2002}, Message: "manual"}
	entities := tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 111}}}
	ingressCtx, cancelIngress := context.WithCancel(context.Background())
	if err := handleMessageEvent(p, ingressCtx, entities, input, false, ""); err != nil {
		t.Fatal(err)
	}
	cancelIngress() // Effect must use the TaskEngine task context, not ingress lifetime.

	st := p.state.Load()
	if st == nil || st.isAFK {
		t.Fatalf("AFK must be off before welcome effect: %+v", st)
	}
	specs := client.snapshot()
	if len(specs) != 1 {
		t.Fatalf("welcome effect admissions=%d, want 1", len(specs))
	}
	spec := specs[0]
	if spec.Scope.Owner != "plugin:afk" || spec.Scope.Generation != scope.Generation() {
		t.Fatalf("wrong effect lifecycle scope: %+v", spec.Scope)
	}
	if spec.Pool != "general" || spec.Class != tasks.PriorityNormal || spec.ExecutionTimeout != afkWelcomeEffectTimeout {
		t.Fatalf("wrong effect resources: pool=%q class=%q timeout=%s", spec.Pool, spec.Class, spec.ExecutionTimeout)
	}
	if effect, ok := spec.Input.(afkWelcomeEffect); !ok || effect.ChatID != 2002 || effect.Peer.AccessHash != 111 {
		t.Fatalf("effect must contain immutable peer facts, got %T %+v", spec.Input, spec.Input)
	}
	if !strings.Contains(string(spec.ID), "afk:welcome:1001:") {
		t.Fatalf("effect task identity=%q", spec.ID)
	}

	finished := make(chan error, 1)
	go func() { finished <- spec.Handler(scope.Context()) }()
	select {
	case <-svc.started:
	case <-time.After(time.Second):
		t.Fatal("welcome effect did not begin sending")
	}
	if p.state.Load().isAFK {
		t.Fatal("AFK became active while Telegram welcome effect was blocked")
	}
	select {
	case err := <-finished:
		t.Fatalf("effect finished before blocked Telegram send was released: %v", err)
	default:
	}
	close(svc.release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("welcome effect did not finish")
	}
}

func TestA2AFKWelcomeRejectionNeverRestoresAFKOrSendsInline(t *testing.T) {
	svc := &mockService{}
	p := New(nil, 1001, func() core.TelegramServicer { return svc })
	p.SetTaskClient(&afkEffectTestClient{reject: errors.New("admission rejected")})
	if err := p.enableAFK(context.Background(), "meeting"); err != nil {
		t.Fatal(err)
	}
	msg := &tg.Message{ID: 54, Out: true, PeerID: &tg.PeerUser{UserID: 2002}, Message: "manual"}
	entities := tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 123}}}
	if err := handleMessageEvent(p, context.Background(), entities, msg, false, ""); err != nil {
		t.Fatal(err)
	}
	if st := p.state.Load(); st == nil || st.isAFK {
		t.Fatalf("admission failure must not roll AFK back: %+v", st)
	}
	svc.mu.Lock()
	sent := len(svc.sentMessages)
	svc.mu.Unlock()
	if sent != 0 {
		t.Fatalf("fallback synchronous send occurred: %d", sent)
	}
}

func TestA2AFKWelcomeEffectHonorsScopeCancellation(t *testing.T) {
	svc := &mockService{}
	p := New(nil, 1001, func() core.TelegramServicer { return svc })
	client := &afkEffectTestClient{}
	p.SetTaskClient(client)
	scope := plugin.NewScope(context.Background(), "plugin:afk")
	if err := p.InitScope(scope.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if err := p.enableAFK(context.Background(), "meeting"); err != nil {
		t.Fatal(err)
	}
	msg := &tg.Message{ID: 55, Out: true, PeerID: &tg.PeerUser{UserID: 2002}, Message: "manual"}
	entities := tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 123}}}
	if err := handleMessageEvent(p, context.Background(), entities, msg, false, ""); err != nil {
		t.Fatal(err)
	}
	specs := client.snapshot()
	if len(specs) != 1 {
		t.Fatalf("effects=%d, want 1", len(specs))
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scope.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if err := specs[0].Handler(scope.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("stale generation effect should be canceled, err=%v", err)
	}
	svc.mu.Lock()
	sent := len(svc.sentMessages)
	svc.mu.Unlock()
	if sent != 0 {
		t.Fatalf("canceled effect sent %d messages", sent)
	}
	if p.state.Load().isAFK {
		t.Fatal("scope cancellation restored AFK")
	}
}

func TestA2AFKManifestRequiresSharedTaskEngine(t *testing.T) {
	for _, capability := range Module.Manifest().Capabilities {
		if capability == plugin.CapTasks {
			return
		}
	}
	t.Fatal("AFK module must declare CapTasks for managed effect admission")
}
