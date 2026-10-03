package filters

import (
	"testing"

	"github.com/inipew/goultroid/internal/core"
)

func TestR6FiltersRegistrationKeepsMatchDecisionSynchronous(t *testing.T) {
	p := newPlugin(nil, nil)

	registrations := p.MessageHookRegistrations()
	if len(registrations) != 1 {
		t.Fatalf("registrations=%d, want 1", len(registrations))
	}
	registration := registrations[0]
	if registration.Routing.Lane != core.MessageHookDecision {
		t.Fatalf("lane=%v, want decision", registration.Routing.Lane)
	}
	if registration.Execution.FailurePolicy != core.MessageHookFailOpen {
		t.Fatalf("failure policy=%v, want fail-open", registration.Execution.FailurePolicy)
	}
	if registration.Execution.Ordering != core.MessageHookOrderingChat {
		t.Fatalf("ordering=%v, want chat", registration.Execution.Ordering)
	}
	if len(registration.Routing.Interests) != 1 {
		t.Fatalf("interests=%d, want 1", len(registration.Routing.Interests))
	}
	interest := registration.Routing.Interests[0]
	if interest.Directions != core.MessageDirectionIncoming ||
		interest.Peers != core.MessagePeerStable ||
		interest.Commands != core.MessagePlain ||
		!interest.RequireText {
		t.Fatalf("unexpected structural interest: %+v", interest)
	}
}
