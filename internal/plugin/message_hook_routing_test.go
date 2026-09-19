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

type canonicalHookTestPlugin struct {
	dummyPlugin
}

func (p *canonicalHookTestPlugin) MessageHookPriority() int { return 20 }
func (p *canonicalHookTestPlugin) HandleMessageEvent(context.Context, *core.MessageEnvelope) error {
	return nil
}
func (p *canonicalHookTestPlugin) MessageHookRouting() core.MessageHookRouting {
	return core.MessageHookRouting{
		Lane: core.MessageHookDecision,
		Interests: []core.MessageHookInterest{{
			Directions: core.MessageDirectionIncoming,
			Peers:      core.MessagePeerGroup,
		}},
	}
}

type canonicalHookTestRegistrar struct {
	routedHookTestRegistrar
	canonicalCalls int
	routing        core.MessageHookRouting
}

func (r *canonicalHookTestRegistrar) AddPrioritizedCanonicalMessageHandlerWithRouting(_ int, routing core.MessageHookRouting, _ CanonicalMessageHookHandler) func() {
	r.canonicalCalls++
	r.routing = routing
	return func() {}
}

func TestManager_PrefersCanonicalMessageHook(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &canonicalHookTestRegistrar{}
	mgr.SetHookRegistrar(registrar)

	p := &canonicalHookTestPlugin{dummyPlugin: dummyPlugin{name: "canonical_hook"}}
	if err := mgr.Register(p); err != nil {
		t.Fatalf("register canonical plugin: %v", err)
	}
	if registrar.canonicalCalls != 1 {
		t.Fatalf("canonical registrar calls=%d, want 1", registrar.canonicalCalls)
	}
	if registrar.legacyCalls != 0 || registrar.routedCalls != 0 {
		t.Fatalf("legacy raw registrar was used: legacy=%d routed=%d", registrar.legacyCalls, registrar.routedCalls)
	}
	if registrar.routing.Lane != core.MessageHookDecision {
		t.Fatalf("unexpected routing: %+v", registrar.routing)
	}
}

func TestManager_RawMessageHookRequiresCapabilityWhenGateConfigured(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &routedHookTestRegistrar{}
	mgr.SetHookRegistrar(registrar)
	gate := NewCapabilityGate()
	gate.SetFailClosed(true)
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil)

	p := &routedHookTestPlugin{dummyPlugin: dummyPlugin{name: "raw_hook"}}
	err := mgr.Register(p)
	if err == nil {
		t.Fatal("expected raw hook registration to be denied without telegram.raw")
	}
	if registrar.routedCalls != 0 || registrar.legacyCalls != 0 {
		t.Fatal("denied raw hook reached dispatcher registrar")
	}
}

func TestManager_RawMessageHookCapabilityCanBeExplicitlyGranted(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &routedHookTestRegistrar{}
	mgr.SetHookRegistrar(registrar)
	gate := NewCapabilityGate()
	gate.SetFailClosed(true)
	gate.AllowPrivileged("raw_allowed", CapTelegramRaw)
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil)

	p := &routedHookTestPlugin{dummyPlugin: dummyPlugin{name: "raw_allowed"}}
	manifest := Manifest{
		ID:           "raw_allowed",
		Name:         "raw_allowed",
		Version:      "1.0.0",
		Capabilities: []string{CapTelegramRaw},
	}
	if err := mgr.RegisterModule(context.Background(), manifest, p); err != nil {
		t.Fatalf("register privileged raw hook: %v", err)
	}
	if registrar.routedCalls != 1 {
		t.Fatalf("raw routed calls=%d, want 1", registrar.routedCalls)
	}
}

func TestManager_CanonicalMessageHookRequiresReadCapabilityWhenGateConfigured(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &canonicalHookTestRegistrar{}
	mgr.SetHookRegistrar(registrar)
	gate := NewCapabilityGate()
	gate.SetFailClosed(true)
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil)

	p := &canonicalHookTestPlugin{dummyPlugin: dummyPlugin{name: "canonical_denied"}}
	err := mgr.Register(p)
	if err == nil {
		t.Fatal("expected canonical hook registration to require telegram.read")
	}
	if registrar.canonicalCalls != 0 {
		t.Fatal("denied canonical hook reached dispatcher registrar")
	}
}

func TestManager_CanonicalMessageHookReadCapabilityDeclared(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &canonicalHookTestRegistrar{}
	mgr.SetHookRegistrar(registrar)
	gate := NewCapabilityGate()
	gate.SetFailClosed(true)
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil)

	p := &canonicalHookTestPlugin{dummyPlugin: dummyPlugin{name: "canonical_allowed"}}
	manifest := Manifest{
		ID:           "canonical_allowed",
		Name:         "canonical_allowed",
		Version:      "1.0.0",
		Capabilities: []string{CapTelegramRead},
	}
	if err := mgr.RegisterModule(context.Background(), manifest, p); err != nil {
		t.Fatalf("register canonical hook with telegram.read: %v", err)
	}
	if registrar.canonicalCalls != 1 {
		t.Fatalf("canonical registrar calls=%d, want 1", registrar.canonicalCalls)
	}
}
