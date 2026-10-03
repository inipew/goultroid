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

type recordingHookRegistrar struct {
	calls        int
	registration core.MessageHookRegistration
}

func (r *recordingHookRegistrar) RegisterMessageHook(registration core.MessageHookRegistration) (func(), error) {
	r.calls++
	r.registration = registration
	return func() {}, nil
}

func TestManager_PrefersIndexedHookRouting(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)

	p := &routedHookTestPlugin{dummyPlugin: dummyPlugin{name: "routed_hook"}}
	if err := mgr.Register(p); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if registrar.calls != 1 {
		t.Fatalf("registrar calls=%d, want 1", registrar.calls)
	}
	registration := registrar.registration
	if registration.RawHandler == nil || registration.Handler != nil || registration.LegacyRouting {
		t.Fatalf("unexpected raw routed registration: %+v", registration)
	}
	if registration.Routing.Lane != core.MessageHookDecision || len(registration.Routing.Interests) != 1 {
		t.Fatalf("unexpected routing metadata: %+v", registration.Routing)
	}
	if registration.Scope.Owner != "plugin:routed_hook" || registration.Scope.Generation == 0 {
		t.Fatalf("unexpected scope: %+v", registration.Scope)
	}
}

type canonicalHookTestPlugin struct {
	dummyPlugin
}

type splitHookTestPlugin struct {
	dummyPlugin
}

func (p *splitHookTestPlugin) MessageHookPriority() int { return 50 }
func (p *splitHookTestPlugin) HandleMessageEvent(context.Context, *core.MessageEnvelope) error {
	return nil
}
func (p *splitHookTestPlugin) MessageHookRegistrations() []core.MessageHookRegistration {
	return []core.MessageHookRegistration{
		{Priority: 50, Routing: core.MessageHookRouting{Lane: core.MessageHookDecision}, Handler: p.HandleMessageEvent},
		{Priority: 50, Routing: core.MessageHookRouting{Lane: core.MessageHookEvent}, Handler: p.HandleMessageEvent},
	}
}

type multiRecordingHookRegistrar struct {
	registrations []core.MessageHookRegistration
	cleaned       int
}

func (r *multiRecordingHookRegistrar) RegisterMessageHook(reg core.MessageHookRegistration) (func(), error) {
	r.registrations = append(r.registrations, reg)
	return func() { r.cleaned++ }, nil
}

func TestManager_RegistersAndCleansUpSplitMessageHooks(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &multiRecordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	p := &splitHookTestPlugin{dummyPlugin: dummyPlugin{name: "split_hook"}}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	if len(registrar.registrations) != 2 ||
		registrar.registrations[0].Routing.Lane != core.MessageHookDecision ||
		registrar.registrations[1].Routing.Lane != core.MessageHookEvent {
		t.Fatalf("unexpected split registrations: %+v", registrar.registrations)
	}
	for _, reg := range registrar.registrations {
		if reg.Scope.Owner != "plugin:split_hook" || reg.Scope.Generation == 0 {
			t.Fatalf("missing plugin scope: %+v", reg.Scope)
		}
	}
	if err := mgr.Disable(context.Background(), "split_hook"); err != nil {
		t.Fatal(err)
	}
	if registrar.cleaned != 2 {
		t.Fatalf("cleaned %d hooks, want 2", registrar.cleaned)
	}
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

func TestManager_PrefersCanonicalMessageHook(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)

	p := &canonicalHookTestPlugin{dummyPlugin: dummyPlugin{name: "canonical_hook"}}
	if err := mgr.Register(p); err != nil {
		t.Fatalf("register canonical plugin: %v", err)
	}
	if registrar.calls != 1 {
		t.Fatalf("canonical registrar calls=%d, want 1", registrar.calls)
	}
	registration := registrar.registration
	if registration.Handler == nil || registration.RawHandler != nil || registration.LegacyRouting {
		t.Fatalf("unexpected canonical registration: %+v", registration)
	}
	if registration.Routing.Lane != core.MessageHookDecision {
		t.Fatalf("unexpected routing: %+v", registration.Routing)
	}
}

func TestManager_RawMessageHookRequiresCapabilityWhenGateConfigured(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	gate := NewCapabilityGate()
	gate.SetFailClosed(true)
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil)

	p := &routedHookTestPlugin{dummyPlugin: dummyPlugin{name: "raw_hook"}}
	err := mgr.Register(p)
	if err == nil {
		t.Fatal("expected raw hook registration to be denied without telegram.raw")
	}
	if registrar.calls != 0 {
		t.Fatal("denied raw hook reached dispatcher registrar")
	}
}

func TestManager_RawMessageHookCapabilityCanBeExplicitlyGranted(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
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
	if registrar.calls != 1 || registrar.registration.RawHandler == nil {
		t.Fatalf("raw registration=%+v calls=%d, want one raw registration", registrar.registration, registrar.calls)
	}
}

func TestManager_CanonicalMessageHookRequiresReadCapabilityWhenGateConfigured(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	gate := NewCapabilityGate()
	gate.SetFailClosed(true)
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil)

	p := &canonicalHookTestPlugin{dummyPlugin: dummyPlugin{name: "canonical_denied"}}
	err := mgr.Register(p)
	if err == nil {
		t.Fatal("expected canonical hook registration to require telegram.read")
	}
	if registrar.calls != 0 {
		t.Fatal("denied canonical hook reached dispatcher registrar")
	}
}

func TestManager_CanonicalMessageHookReadCapabilityDeclared(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
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
	if registrar.calls != 1 || registrar.registration.Handler == nil {
		t.Fatalf("canonical registration=%+v calls=%d, want one canonical registration", registrar.registration, registrar.calls)
	}
}
