package plugin

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/core"
)

type implicitStateHookTestPlugin struct {
	dummyPlugin
}

func (p *implicitStateHookTestPlugin) MessageHookPriority() int { return 10 }
func (p *implicitStateHookTestPlugin) MessageHookInterested(chatID int64) bool {
	return chatID == 42
}
func (p *implicitStateHookTestPlugin) handle(context.Context, *core.MessageEnvelope) error {
	return nil
}
func (p *implicitStateHookTestPlugin) MessageHookRegistrations() []core.MessageHookRegistration {
	return []core.MessageHookRegistration{{
		Routing: core.MessageHookRouting{Lane: core.MessageHookDecision},
		Handler: p.handle,
	}}
}

func TestManager_DoesNotInferStateGateFromLegacyHelperMethod(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)
	p := &implicitStateHookTestPlugin{dummyPlugin: dummyPlugin{name: "implicit_state"}}
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	if registrar.calls != 1 {
		t.Fatalf("registrar calls=%d, want 1", registrar.calls)
	}
	if registrar.registration.StateGate != nil {
		t.Fatal("manager inferred a state gate outside MessageHookRegistration")
	}
}
