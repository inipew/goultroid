package plugin

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type routedHookTestPlugin struct {
	dummyPlugin
}

func (p *routedHookTestPlugin) MessageHookPriority() int { return 10 }
func (p *routedHookTestPlugin) HandleIncomingMessage(context.Context, tg.Entities, *tg.Message, bool, string) error {
	return nil
}
func (p *routedHookTestPlugin) MessageHookRouting() core.MessageHookRouting {
	return core.MessageHookRouting{
		Lane: core.MessageHookDecision,
		Interests: []core.MessageHookInterest{{
			Directions: core.MessageDirectionIncoming,
			Peers:      core.MessagePeerPrivate,
		}},
	}
}

type routedHookTestRegistrar struct {
	legacyCalls int
	routedCalls int
	routing     core.MessageHookRouting
}

func (r *routedHookTestRegistrar) AddPrioritizedMessageHandler(int, MessageHookHandler) func() {
	r.legacyCalls++
	return func() {}
}
func (r *routedHookTestRegistrar) AddPrioritizedMessageHandlerWithRouting(_ int, routing core.MessageHookRouting, _ MessageHookHandler) func() {
	r.routedCalls++
	r.routing = routing
	return func() {}
}

func TestManager_PrefersIndexedHookRouting(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &routedHookTestRegistrar{}
	mgr.SetHookRegistrar(registrar)

	p := &routedHookTestPlugin{dummyPlugin: dummyPlugin{name: "routed_hook"}}
	if err := mgr.Register(p); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if registrar.routedCalls != 1 || registrar.legacyCalls != 0 {
		t.Fatalf("registrar calls routed=%d legacy=%d, want 1/0", registrar.routedCalls, registrar.legacyCalls)
	}
	if registrar.routing.Lane != core.MessageHookDecision || len(registrar.routing.Interests) != 1 {
		t.Fatalf("unexpected routing metadata: %+v", registrar.routing)
	}
}
