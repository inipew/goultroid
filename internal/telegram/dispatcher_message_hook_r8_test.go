package telegram

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type r8LifecycleHookPlugin struct {
	decisionCount atomic.Int32
	eventCount    atomic.Int32
}

func (*r8LifecycleHookPlugin) Name() string             { return "r8-lifecycle" }
func (*r8LifecycleHookPlugin) Commands() []core.Command { return nil }
func (*r8LifecycleHookPlugin) Init() error              { return nil }
func (*r8LifecycleHookPlugin) MessageHookPriority() int { return 50 }

func (p *r8LifecycleHookPlugin) MessageHookRegistrations() []core.MessageHookRegistration {
	return []core.MessageHookRegistration{
		{
			Priority: p.MessageHookPriority(),
			Routing: core.MessageHookRouting{
				Lane: core.MessageHookDecision,
				Interests: []core.MessageHookInterest{{
					Directions: core.MessageDirectionOutgoing,
					Peers:      core.MessagePeerStable,
				}},
			},
			Execution: core.MessageHookExecutionPolicy{
				FailurePolicy: core.MessageHookFailOpen,
				Ordering:      core.MessageHookOrderingChat,
			},
			Handler: func(context.Context, *core.MessageEnvelope) error {
				p.decisionCount.Add(1)
				return nil
			},
		},
		{
			Priority: p.MessageHookPriority(),
			Routing: core.MessageHookRouting{
				Lane: core.MessageHookEvent,
				Interests: []core.MessageHookInterest{{
					Directions: core.MessageDirectionIncoming,
					Peers:      core.MessagePeerPrivate,
				}},
			},
			Execution: core.MessageHookExecutionPolicy{
				FailurePolicy: core.MessageHookFailOpen,
				Ordering:      core.MessageHookOrderingPlugin,
			},
			Handler: func(context.Context, *core.MessageEnvelope) error {
				p.eventCount.Add(1)
				return nil
			},
		},
	}
}

func r8WaitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}

func r8WaitForTaskEngineSettling(t *testing.T, engine *taskengine.Engine, terminalCap int) {
	t.Helper()
	r8WaitFor(t, 2*time.Second, func() bool {
		stats, err := engine.Stats(context.Background())
		if err != nil {
			return false
		}
		general := stats.Pools[tasks.PoolID("general")]
		interactive := stats.Pools[tasks.PoolID("interactive")]
		return stats.ActiveTasks == 0 &&
			general.Waiting == 0 &&
			general.Running == 0 &&
			general.Dispatching == 0 &&
			general.Workers == 0 &&
			interactive.Waiting == 0 &&
			interactive.Running == 0 &&
			interactive.Dispatching == 0 &&
			interactive.Workers == 0 &&
			stats.TerminalCount <= terminalCap &&
			stats.RetainedBytes <= stats.RetainedCap
	}, "TaskEngine did not settle to bounded zero-idle state")
}

func TestR8MessageHookBurstReloadAndResourceSettling(t *testing.T) {
	const ownerID int64 = 1001

	cfg := taskengine.NewDefaultConfig()
	for _, poolID := range []tasks.PoolID{"general", "interactive"} {
		pool := cfg.Pools[poolID]
		pool.ZeroIdle = true
		pool.MinConcurrency = 0
		pool.IdleTimeout = 10 * time.Millisecond
		cfg.Pools[poolID] = pool
	}
	cfg.ResultCapacity = 64
	cfg.MaxTerminalRetained = 16
	cfg.TerminalTTL = 100 * time.Millisecond

	engine := taskengine.NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	dispatcher.SetTasks(engine)
	dispatcher.SetSelfID(ownerID)

	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(engine)

	p := &r8LifecycleHookPlugin{}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	enabled := true
	defer func() {
		if enabled {
			_ = mgr.Disable(context.Background(), p.Name())
		}
	}()

	oldScope, ok := mgr.Scope(p.Name())
	if !ok || oldScope == nil {
		t.Fatal("initial plugin scope missing")
	}
	oldGeneration := oldScope.Generation()

	const burst = 24
	for i := 0; i < burst; i++ {
		if err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      9000 + i,
				Out:     true,
				Message: "manual",
				PeerID:  &tg.PeerUser{UserID: 2002},
				FromID:  &tg.PeerUser{UserID: ownerID},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	for i := 0; i < burst; i++ {
		if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      10000 + i,
				Message: "incoming",
				PeerID:  &tg.PeerUser{UserID: 2002},
				FromID:  &tg.PeerUser{UserID: 2002},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if got := p.decisionCount.Load(); got != burst {
		t.Fatalf("decision count=%d, want %d", got, burst)
	}
	r8WaitFor(t, time.Second, func() bool {
		return p.eventCount.Load() == burst
	}, "event burst did not drain")
	r8WaitForTaskEngineSettling(t, engine, cfg.MaxTerminalRetained)

	if err := mgr.Disable(context.Background(), p.Name()); err != nil {
		t.Fatal(err)
	}
	enabled = false
	decisionBefore := p.decisionCount.Load()
	eventBefore := p.eventCount.Load()

	if err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      11001,
			Out:     true,
			Message: "disabled",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: ownerID},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      11002,
			Message: "disabled",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if p.decisionCount.Load() != decisionBefore || p.eventCount.Load() != eventBefore {
		t.Fatal("disabled plugin retained stale message-hook registration")
	}

	if err := mgr.Enable(context.Background(), p.Name()); err != nil {
		t.Fatal(err)
	}
	enabled = true
	newScope, ok := mgr.Scope(p.Name())
	if !ok || newScope == nil {
		t.Fatal("re-enabled plugin scope missing")
	}
	if newScope.Generation() == 0 || newScope.Generation() == oldGeneration {
		t.Fatalf("generation=%d after reload, want new generation distinct from %d", newScope.Generation(), oldGeneration)
	}

	if err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      12001,
			Out:     true,
			Message: "re-enabled",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: ownerID},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      12002,
			Message: "re-enabled",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if p.decisionCount.Load() != decisionBefore+1 {
		t.Fatalf("re-enabled decision count=%d, want %d", p.decisionCount.Load(), decisionBefore+1)
	}
	r8WaitFor(t, time.Second, func() bool {
		return p.eventCount.Load() == eventBefore+1
	}, "re-enabled event hook did not run exactly once")

	if err := mgr.Disable(context.Background(), p.Name()); err != nil {
		t.Fatal(err)
	}
	enabled = false
	r8WaitForTaskEngineSettling(t, engine, cfg.MaxTerminalRetained)

	stats, err := engine.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.ScopeTombstones > 4 {
		t.Fatalf("scope tombstones=%d after one reload cycle, want bounded small state", stats.ScopeTombstones)
	}
}
