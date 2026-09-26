package plugin

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
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

func TestManager_PrefersStateAwareIndexedHookRouting(t *testing.T) {
	mgr := NewManager(core.NewRouter("."))
	registrar := &recordingHookRegistrar{}
	mgr.SetHookRegistrar(registrar)

	p := &statefulHookTestPlugin{dummyPlugin: dummyPlugin{name: "stateful_hook"}}
	if err := mgr.Register(p); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	if registrar.calls != 1 {
		t.Fatalf("registrar calls=%d, want 1", registrar.calls)
	}
	registration := registrar.registration
	if registration.RawHandler == nil || registration.Handler != nil || registration.LegacyRouting {
		t.Fatalf("unexpected stateful raw registration: %+v", registration)
	}
	if registration.StateGate == nil {
		t.Fatal("state gate was not propagated")
	}
	if registration.StateGate(7) || !registration.StateGate(42) {
		t.Fatal("propagated state gate returned unexpected result")
	}
	if registration.Routing.Lane != core.MessageHookDecision {
		t.Fatalf("unexpected lane: %v", registration.Routing.Lane)
	}
}
