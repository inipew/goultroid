package telegram

import (
	"testing"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestR1MessageHookOrderingDomains(t *testing.T) {
	decision := messageHookDecisionOrderingKey(42)
	if decision != "msg-decision:chat:42" {
		t.Fatalf("decision ordering key=%q", decision)
	}

	afkScope := tasks.ScopeIdentity{Owner: "plugin:afk", Generation: 1}
	userlogScope := tasks.ScopeIdentity{Owner: "plugin:userlog", Generation: 7}
	afkEvent := messageHookEventOrderingKey(afkScope, 42)
	if afkEvent != "msg-event:plugin:afk:chat:42" {
		t.Fatalf("AFK event ordering key=%q", afkEvent)
	}
	if decision == afkEvent {
		t.Fatalf("decision and event domains collide on %q", decision)
	}
	if other := messageHookEventOrderingKey(userlogScope, 42); other == afkEvent {
		t.Fatalf("different plugin event domains collide on %q", other)
	}
	if sameOwnerNewGeneration := messageHookEventOrderingKey(
		tasks.ScopeIdentity{Owner: "plugin:afk", Generation: 2},
		42,
	); sameOwnerNewGeneration != afkEvent {
		t.Fatalf("event ordering changed across lifecycle generations: old=%q new=%q", afkEvent, sameOwnerNewGeneration)
	}
	if otherChat := messageHookEventOrderingKey(afkScope, 43); otherChat == afkEvent {
		t.Fatalf("different chats reused event ordering key %q", otherChat)
	}
	if unscoped := messageHookEventOrderingKey(tasks.ScopeIdentity{}, 42); unscoped != "msg-event:unscoped:chat:42" {
		t.Fatalf("unscoped event ordering key=%q", unscoped)
	}
}

func TestR1StalledEventOrderingDomainDoesNotBlockDecision(t *testing.T) {
	ctrl := admission.NewController(map[tasks.PoolID]admission.PoolConfig{
		"interactive": {BacklogLimit: 8, PayloadBudget: 1 << 20},
		"general":     {BacklogLimit: 8, PayloadBudget: 1 << 20},
	})

	eventKey := messageHookEventOrderingKey(tasks.ScopeIdentity{Owner: "plugin:userlog", Generation: 1}, 42)
	decisionKey := messageHookDecisionOrderingKey(42)
	if eventKey == decisionKey {
		t.Fatalf("test requires distinct ordering domains, got %q", eventKey)
	}

	event1 := tasks.WorkSpec{
		ID: "event-1", QuotaOwner: "plugin:userlog", Pool: "general",
		Class: tasks.PriorityBackground, OrderingKey: eventKey,
	}
	event2 := tasks.WorkSpec{
		ID: "event-2", QuotaOwner: "plugin:userlog", Pool: "general",
		Class: tasks.PriorityBackground, OrderingKey: eventKey,
	}
	decision := tasks.WorkSpec{
		ID: "decision-1", QuotaOwner: "plugin:afk", Pool: "interactive",
		Class: tasks.PriorityInteractive, OrderingKey: decisionKey,
	}

	ctrl.Enqueue(&admission.QueueEntry{Spec: event1})
	activeEvent, err := ctrl.SelectCandidate("general")
	if err != nil || activeEvent.Spec.ID != event1.ID {
		t.Fatalf("active event=%v err=%v, want event-1", activeEvent, err)
	}

	ctrl.Enqueue(&admission.QueueEntry{Spec: event2})
	ctrl.Enqueue(&admission.QueueEntry{Spec: decision})

	if _, err := ctrl.SelectCandidate("general"); err != admission.ErrNoEligibleTask {
		t.Fatalf("same-domain event should remain blocked, err=%v", err)
	}
	selectedDecision, err := ctrl.SelectCandidate("interactive")
	if err != nil || selectedDecision.Spec.ID != decision.ID {
		t.Fatalf("decision candidate=%v err=%v, want decision-1 while event domain is locked", selectedDecision, err)
	}

	ctrl.OnTaskTerminal(selectedDecision.Spec)
	ctrl.OnTaskTerminal(activeEvent.Spec)
	selectedEvent, err := ctrl.SelectCandidate("general")
	if err != nil || selectedEvent.Spec.ID != event2.ID {
		t.Fatalf("event candidate after unlock=%v err=%v, want event-2", selectedEvent, err)
	}
}
