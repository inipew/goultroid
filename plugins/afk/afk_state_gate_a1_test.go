package afk

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/core"
)

func TestA1AFKRegistrationStateGates(t *testing.T) {
	p := NewWithService(nil, 42, nil)
	registrations := p.MessageHookRegistrations()
	if len(registrations) != 2 {
		t.Fatalf("AFK should register outgoing decision and incoming event hooks, got %d", len(registrations))
	}

	outgoing, incoming := registrations[0], registrations[1]
	if outgoing.Routing.Lane != core.MessageHookDecision || incoming.Routing.Lane != core.MessageHookEvent {
		t.Fatalf("AFK execution lanes changed: outgoing=%v incoming=%v", outgoing.Routing.Lane, incoming.Routing.Lane)
	}
	if outgoing.StateGate == nil || incoming.StateGate == nil {
		t.Fatal("both AFK registrations must provide cheap state gates")
	}
	assertGates := func(outgoingExpected, incomingExpected bool) {
		t.Helper()
		if got := outgoing.StateGate(100); got != outgoingExpected {
			t.Errorf("outgoing state gate = %t, want %t", got, outgoingExpected)
		}
		if got := incoming.StateGate(100); got != incomingExpected {
			t.Errorf("incoming state gate = %t, want %t", got, incomingExpected)
		}
	}

	assertGates(false, false) // Initial inactive: no unnecessary TaskEngine admission.
	if err := p.enableAFK(context.Background(), "meeting"); err != nil {
		t.Fatalf("enable AFK: %v", err)
	}
	assertGates(true, true) // Active: outgoing must still auto-unAFK.

	p.SetAutoReply(false)
	assertGates(true, false) // Auto-reply off must not suppress the outgoing transition.

	if _, changed, err := p.disableAFK(context.Background()); err != nil || !changed {
		t.Fatalf("disable AFK: changed=%t err=%v", changed, err)
	}
	assertGates(false, false)

	p.SetAutoReply(true)
	assertGates(false, false) // Auto-reply alone must never admit an inactive AFK hook.
}
