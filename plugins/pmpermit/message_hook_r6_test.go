package pmpermit_test

import (
	"testing"

	"github.com/inipew/goultroid/internal/core"
	pmpermitservice "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/plugins/pmpermit"
	"go.uber.org/zap"
)

func TestR6PMPermitRegistrationsSeparateIncomingEnforcementAndOutgoingTransition(t *testing.T) {
	svc := pmpermitservice.NewService(nil, nil, 1, core.NewPermissions(1, nil), zap.NewNop())
	p := pmpermit.New(svc)

	registrations := p.MessageHookRegistrations()
	if len(registrations) != 2 {
		t.Fatalf("registrations=%d, want incoming + outgoing", len(registrations))
	}
	incoming, outgoing := registrations[0], registrations[1]
	if incoming.Routing.Lane != core.MessageHookDecision ||
		incoming.Execution.FailurePolicy != core.MessageHookFailClosed ||
		incoming.Execution.Ordering != core.MessageHookOrderingChat {
		t.Fatalf("incoming execution contract=%+v routing=%+v", incoming.Execution, incoming.Routing)
	}
	if outgoing.Routing.Lane != core.MessageHookDecision ||
		outgoing.Execution.FailurePolicy != core.MessageHookFailOpen ||
		outgoing.Execution.Ordering != core.MessageHookOrderingChat {
		t.Fatalf("outgoing execution contract=%+v routing=%+v", outgoing.Execution, outgoing.Routing)
	}
	if outgoing.FastGate == nil {
		t.Fatal("outgoing PMPermit registration has no pure fast gate")
	}
	if outgoing.FastGate(core.MessageHookFacts{Origin: core.ExecutionAutomation}) {
		t.Fatal("automation-origin outgoing message passed PMPermit fast gate")
	}
	if outgoing.FastGate(core.MessageHookFacts{IsCommand: true}) {
		t.Fatal("outgoing command passed PMPermit fast gate")
	}
	if !outgoing.FastGate(core.MessageHookFacts{Origin: core.ExecutionInteractive}) {
		t.Fatal("manual outgoing private message was rejected by PMPermit fast gate")
	}
}
