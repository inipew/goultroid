package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

type recordingHookRegistrar struct {
	calls         int
	registration  core.MessageHookRegistration
	registrations []core.MessageHookRegistration
	cleaned       int
}

func (r *recordingHookRegistrar) RegisterMessageHook(registration core.MessageHookRegistration) (func(), error) {
	r.calls++
	r.registration = registration
	r.registrations = append(r.registrations, registration)
	return func() { r.cleaned++ }, nil
}

type explicitHookTestPlugin struct {
	dummyPlugin
	registrations []core.MessageHookRegistration
}

func (p *explicitHookTestPlugin) MessageHookPriority() int { return 50 }
func (p *explicitHookTestPlugin) MessageHookRegistrations() []core.MessageHookRegistration {
	return p.registrations
}
func (p *explicitHookTestPlugin) handle(context.Context, *core.MessageEnvelope) error { return nil }

func newExplicitHookTestPlugin(name string) *explicitHookTestPlugin {
	p := &explicitHookTestPlugin{dummyPlugin: dummyPlugin{name: name}}
	p.registrations = []core.MessageHookRegistration{
		{
			Routing: core.MessageHookRouting{Lane: core.MessageHookDecision},
			StateGate: func(chatID int64) bool {
				return chatID == 42
			},
			Execution: core.MessageHookExecutionPolicy{FailurePolicy: core.MessageHookFailClosed},
			Handler:   p.handle,
		},
		{
			Routing: core.MessageHookRouting{Lane: core.MessageHookEvent},
			StateGate: func(chatID int64) bool {
				return chatID == 7
			},
			Handler: p.handle,
		},
	}
	return p
}

func TestManager_RegistersAndCleansUpExplicitMessageHooks(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	p := newExplicitHookTestPlugin("explicit_hook")
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	if len(registrar.registrations) != 2 {
		t.Fatalf("registrations=%d, want 2", len(registrar.registrations))
	}
	for _, reg := range registrar.registrations {
		if reg.Scope.Owner != "plugin:explicit_hook" || reg.Scope.Generation == 0 {
			t.Fatalf("missing plugin scope: %+v", reg.Scope)
		}
		if reg.Priority != 50 {
			t.Fatalf("normalized priority=%d, want 50", reg.Priority)
		}
		if reg.Execution.HandlerTimeout != 5*time.Second {
			t.Fatalf("handler timeout=%s, want 5s", reg.Execution.HandlerTimeout)
		}
	}
	decision := registrar.registrations[0]
	if decision.StateGate == nil || decision.StateGate(7) || !decision.StateGate(42) {
		t.Fatal("explicit decision state gate was not preserved")
	}
	if decision.Execution.FailurePolicy != core.MessageHookFailClosed ||
		decision.Execution.TaskTimeout != 5*time.Second ||
		decision.Execution.Ordering != core.MessageHookOrderingChat {
		t.Fatalf("unexpected decision execution policy: %+v", decision.Execution)
	}
	event := registrar.registrations[1]
	if event.StateGate == nil || !event.StateGate(7) || event.StateGate(42) {
		t.Fatal("explicit event state gate was not preserved")
	}
	if event.Execution.FailurePolicy != core.MessageHookFailOpen ||
		event.Execution.TaskTimeout != 10*time.Second ||
		event.Execution.Ordering != core.MessageHookOrderingPluginChat {
		t.Fatalf("unexpected event execution policy: %+v", event.Execution)
	}
	if err := mgr.Disable(context.Background(), "explicit_hook"); err != nil {
		t.Fatal(err)
	}
	if registrar.cleaned != 2 {
		t.Fatalf("cleaned=%d hooks, want 2", registrar.cleaned)
	}
}

func TestManager_RejectsEmptyExplicitMessageHooks(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	p := &explicitHookTestPlugin{dummyPlugin: dummyPlugin{name: "empty_explicit"}}
	if err := mgr.Register(p); err == nil {
		t.Fatal("expected empty registration set to fail")
	}
	if registrar.calls != 0 {
		t.Fatalf("empty registrations reached registrar: %d", registrar.calls)
	}
}

type failingHookRegistrar struct {
	calls    int
	cleaned  int
	failCall int
}

func (r *failingHookRegistrar) RegisterMessageHook(core.MessageHookRegistration) (func(), error) {
	r.calls++
	call := r.calls
	if call == r.failCall {
		return nil, errors.New("forced registration failure")
	}
	return func() {
		if call < r.failCall {
			r.cleaned++
		}
	}, nil
}

func TestManager_RollsBackPartialExplicitMessageHookRegistration(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &failingHookRegistrar{failCall: 2}
	mgr.SetHookRegistrar(registrar)
	p := newExplicitHookTestPlugin("partial_explicit")
	if err := mgr.Register(p); err == nil {
		t.Fatal("expected registration failure")
	}
	if registrar.calls != 2 || registrar.cleaned != 1 {
		t.Fatalf("calls=%d cleaned=%d, want 2/1", registrar.calls, registrar.cleaned)
	}
}

func TestManager_RejectsInvalidExplicitExecutionPolicy(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	p := newExplicitHookTestPlugin("invalid_explicit")
	p.registrations = []core.MessageHookRegistration{{
		Routing:   core.MessageHookRouting{Lane: core.MessageHookDecision},
		Execution: core.MessageHookExecutionPolicy{TaskTimeout: -time.Second},
		Handler:   p.handle,
	}}
	if err := mgr.Register(p); err == nil {
		t.Fatal("expected invalid execution policy to fail")
	}
	if registrar.calls != 0 {
		t.Fatal("invalid registration reached registrar")
	}
}

type legacySingleHookTestPlugin struct{ dummyPlugin }

func (p *legacySingleHookTestPlugin) MessageHookPriority() int { return 20 }
func (p *legacySingleHookTestPlugin) HandleMessageEvent(context.Context, *core.MessageEnvelope) error {
	return nil
}
func (p *legacySingleHookTestPlugin) MessageHookRouting() core.MessageHookRouting {
	return core.MessageHookRouting{Lane: core.MessageHookDecision}
}

func TestManager_DoesNotAdaptLegacySingleHookInterfaces(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	p := &legacySingleHookTestPlugin{dummyPlugin: dummyPlugin{name: "legacy_single"}}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	if registrar.calls != 0 {
		t.Fatalf("legacy single-hook adapter still registered %d hook(s)", registrar.calls)
	}
}

func TestManager_ExplicitHooksRequireReadCapabilityWhenGateConfigured(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	gate := NewCapabilityGate()
	gate.SetFailClosed(true)
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil)
	p := newExplicitHookTestPlugin("explicit_denied")
	if err := mgr.Register(p); err == nil {
		t.Fatal("expected telegram.read capability requirement")
	}
	if registrar.calls != 0 {
		t.Fatal("denied hook reached registrar")
	}
}

func TestManager_ExplicitHookReadCapabilityDeclared(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	gate := NewCapabilityGate()
	gate.SetFailClosed(true)
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil)
	p := newExplicitHookTestPlugin("explicit_allowed")
	manifest := Manifest{
		ID:           "explicit_allowed",
		Name:         "explicit_allowed",
		Version:      "1.0.0",
		Capabilities: []string{CapTelegramRead},
	}
	if err := mgr.RegisterModule(context.Background(), manifest, p); err != nil {
		t.Fatal(err)
	}
	if registrar.calls != 2 {
		t.Fatalf("registrar calls=%d, want 2", registrar.calls)
	}
}
