package plugin

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
)

type statefulHookTestPlugin struct {
	dummyPlugin
}

func (p *statefulHookTestPlugin) MessageHookPriority() int { return 10 }
func (p *statefulHookTestPlugin) HandleIncomingMessage(context.Context, tg.Entities, *tg.Message, bool, string) error {
	return nil
}
func (p *statefulHookTestPlugin) MessageHookRouting() core.MessageHookRouting {
	return core.MessageHookRouting{
		Lane: core.MessageHookDecision,
		Interests: []core.MessageHookInterest{{
			Directions: core.MessageDirectionIncoming,
			Peers:      core.MessagePeerGroup,
		}},
	}
}
func (p *statefulHookTestPlugin) MessageHookInterested(chatID int64) bool {
	return chatID == 42
}

type statefulHookTestRegistrar struct {
	legacyCalls int
	stateCalls  int
	stateGate   func(int64) bool
	routing     core.MessageHookRouting
}

func (r *statefulHookTestRegistrar) AddPrioritizedMessageHandler(int, MessageHookHandler) func() {
	r.legacyCalls++
	return func() {}
}
func (r *statefulHookTestRegistrar) AddScopedMessageHandlerWithRoutingAndState(_ int, _ tasks.ScopeIdentity, routing core.MessageHookRouting, stateGate func(int64) bool, _ MessageHookHandler) func() {
	r.stateCalls++
	r.routing = routing
	r.stateGate = stateGate
	return func() {}
}

func TestManager_PrefersStateAwareIndexedHookRouting(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &statefulHookTestRegistrar{}
	mgr.SetHookRegistrar(registrar)

	p := &statefulHookTestPlugin{dummyPlugin: dummyPlugin{name: "stateful_hook"}}
	if err := mgr.Register(p); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if registrar.stateCalls != 1 || registrar.legacyCalls != 0 {
		t.Fatalf("registrar calls state=%d legacy=%d, want 1/0", registrar.stateCalls, registrar.legacyCalls)
	}
	if registrar.stateGate == nil {
		t.Fatal("state gate was not propagated")
	}
	if registrar.stateGate(7) || !registrar.stateGate(42) {
		t.Fatal("propagated state gate returned unexpected result")
	}
	if registrar.routing.Lane != core.MessageHookDecision {
		t.Fatalf("unexpected lane: %v", registrar.routing.Lane)
	}
}
