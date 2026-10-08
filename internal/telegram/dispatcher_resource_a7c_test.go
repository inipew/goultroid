package telegram

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/idempotency"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

// A7-C exercises the production Dispatcher and single shared TaskEngine.
// The observer is deliberately blocked while interactive decisions and
// duplicate canonical callback ingress continue on their original paths.
func TestA7CMixedObserverBurstIsolatedFromSecurityAndCallbackClaims(t *testing.T) {
	ctx := context.Background()
	baselineGoroutines := runtime.NumGoroutine()
	var baseline, peak, settled runtime.MemStats
	runtime.ReadMemStats(&baseline)

	engine := taskengine.NewEngine(taskengine.NewDefaultConfig())
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var closeOnce sync.Once
	var stopOnce sync.Once
	shutdown := func() {
		stopOnce.Do(func() {
			closeOnce.Do(func() { close(release) })
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := engine.Stop(stopCtx); err != nil {
				t.Error(err)
			}
		})
	}
	defer shutdown()

	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1001, nil), nil, zap.NewNop())
	d.SetTasks(engine)
	native := &callbackNativeStub{handled: true}
	answers := newCallbackRecordingService()
	d.SetNativeInteractions(native)
	d.SetService(answers)
	d.SetIdempotency(idempotency.NewManager(time.Minute))

	started := make(chan struct{})
	var observerStarted atomic.Bool
	observer := prioritizedHandler{
		id: 4101, priority: PriorityObservability,
		failurePolicy: FailurePolicyFailOpen,
		routing:       core.MessageHookRouting{Lane: core.MessageHookEvent},
		scope:         tasks.ScopeIdentity{Owner: "plugin:userlog", Generation: 1},
		canonicalHandler: func(taskCtx context.Context, _ *core.MessageEnvelope) error {
			if observerStarted.CompareAndSwap(false, true) {
				close(started)
			}
			select {
			case <-release:
				return nil
			case <-taskCtx.Done():
				return taskCtx.Err()
			}
		},
	}
	var decisions atomic.Int64
	security := prioritizedHandler{
		id: 4102, priority: PrioritySecurity,
		failurePolicy: FailurePolicyFailClosed,
		routing:       core.MessageHookRouting{Lane: core.MessageHookDecision},
		scope:         tasks.ScopeIdentity{Owner: "plugin:pmpermit", Generation: 1},
		canonicalHandler: func(context.Context, *core.MessageEnvelope) error {
			decisions.Add(1)
			return nil
		},
	}
	newMessage := func(id int) (*tg.Message, *core.MessageEnvelope) {
		message := &tg.Message{ID: id, PeerID: &tg.PeerChat{ChatID: 42},
			FromID: &tg.PeerUser{UserID: 2002}, Message: "private-content"}
		return message, NormalizeMessageEnvelope(tg.Entities{}, message, false, "", 1001)
	}
	msg, envelope := newMessage(1)
	d.dispatchEventHandlersEnvelope(ctx, []prioritizedHandler{observer}, envelope, tg.Entities{}, msg)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("observer never occupied the general TaskEngine lane")
	}

	const observerBurst, securityBurst, callbackBurst = 96, 64, 128
	for i := 0; i < observerBurst; i++ {
		msg, envelope = newMessage(1000 + i)
		d.dispatchEventHandlersEnvelope(ctx, []prioritizedHandler{observer}, envelope, tg.Entities{}, msg)
	}
	for i := 0; i < securityBurst; i++ {
		msg, envelope = newMessage(3000 + i)
		if handled := d.executeDecisionHandlersEnvelope(ctx, []prioritizedHandler{security},
			envelope, tg.Entities{}, msg); handled {
			t.Fatalf("security decision %d failed closed due to unrelated observer pressure", i)
		}
	}
	if got := decisions.Load(); got != securityBurst {
		t.Fatalf("security tasks=%d want=%d while observer was blocked", got, securityBurst)
	}
	callback := &tg.UpdateBotCallbackQuery{
		QueryID: 50001, UserID: 1001, Peer: &tg.PeerChat{ChatID: 42},
		MsgID: 55, Data: []byte("a2:test:valid.1"),
	}
	for i := 0; i < callbackBurst; i++ {
		if err := d.OnBotCallbackQuery(ctx, tg.Entities{}, callback); err != nil {
			t.Fatalf("callback duplicate #%d: %v", i, err)
		}
	}
	if got := native.calls.Load(); got != 1 {
		t.Fatalf("duplicate callbacks executed canonical handler %d times, want 1", got)
	}
	if got := answers.callCount.Load(); got == 0 {
		t.Fatal("duplicate callback burst did not receive duplicate ACKs")
	}
	runtime.ReadMemStats(&peak)
	shutdown()
	runtime.GC()
	runtime.ReadMemStats(&settled)
	t.Logf("A7-C mixed ingress: go=%s goroutines baseline=%d settled=%d; heap baseline=%d peak=%d settled=%d bytes; observer attempts=%d security=%d callback attempts=%d",
		runtime.Version(), baselineGoroutines, runtime.NumGoroutine(),
		baseline.HeapAlloc, peak.HeapAlloc, settled.HeapAlloc,
		observerBurst+1, securityBurst, callbackBurst)
}
