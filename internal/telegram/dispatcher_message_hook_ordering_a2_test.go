package telegram

import (
	"testing"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestA2MessageHookOrderingDomains(t *testing.T) {
	decision := messageHookDecisionOrderingKey(42)
	if decision != "msg-decision:chat:42" {
		t.Fatalf("decision key=%q", decision)
	}
	afk := tasks.ScopeIdentity{Owner: "plugin:afk", Generation: 1}
	log := tasks.ScopeIdentity{Owner: "plugin:userlog", Generation: 1}
	event := messageHookEventOrderingKey(afk, 42)
	if event != "msg-event:plugin:afk:chat:42" {
		t.Fatalf("AFK event key=%q", event)
	}
	if event == decision || event == messageHookEventOrderingKey(log, 42) {
		t.Fatal("decision/event or separate plugin event ordering domains collide")
	}
	if event != messageHookEventOrderingKey(tasks.ScopeIdentity{Owner: afk.Owner, Generation: 2}, 42) {
		t.Fatal("event ordering must be stable across plugin generation changes")
	}
	if event == messageHookEventOrderingKey(afk, 43) {
		t.Fatal("different chats must have different event ordering keys")
	}
	if key := messageHookEventOrderingKey(tasks.ScopeIdentity{}, 42); key != "msg-event:unscoped:chat:42" {
		t.Fatalf("unscoped key=%q", key)
	}
}

func TestA2StalledEventDoesNotBlockDecisionAdmission(t *testing.T) {
	ctrl := admission.NewController(map[tasks.PoolID]admission.PoolConfig{
		"interactive": {BacklogLimit: 8, PayloadBudget: 1 << 20},
		"general":     {BacklogLimit: 8, PayloadBudget: 1 << 20},
	})
	eventKey := messageHookEventOrderingKey(tasks.ScopeIdentity{Owner: "plugin:userlog", Generation: 1}, 42)
	decisionKey := messageHookDecisionOrderingKey(42)
	event1 := tasks.WorkSpec{ID: "event-1", QuotaOwner: "plugin:userlog", Pool: "general", Class: tasks.PriorityBackground, OrderingKey: eventKey}
	event2 := tasks.WorkSpec{ID: "event-2", QuotaOwner: "plugin:userlog", Pool: "general", Class: tasks.PriorityBackground, OrderingKey: eventKey}
	decision := tasks.WorkSpec{ID: "decision-1", QuotaOwner: "plugin:afk", Pool: "interactive", Class: tasks.PriorityInteractive, OrderingKey: decisionKey}

	ctrl.Enqueue(&admission.QueueEntry{Spec: event1})
	active, err := ctrl.SelectCandidate("general")
	if err != nil || active.Spec.ID != event1.ID {
		t.Fatalf("active event=%v err=%v", active, err)
	}
	ctrl.Enqueue(&admission.QueueEntry{Spec: event2})
	ctrl.Enqueue(&admission.QueueEntry{Spec: decision})
	if _, err := ctrl.SelectCandidate("general"); err != admission.ErrNoEligibleTask {
		t.Fatalf("same-domain second event must wait; err=%v", err)
	}
	selected, err := ctrl.SelectCandidate("interactive")
	if err != nil || selected.Spec.ID != decision.ID {
		t.Fatalf("decision blocked by active event: selected=%v err=%v", selected, err)
	}
	ctrl.OnTaskTerminal(selected.Spec)
	ctrl.OnTaskTerminal(active.Spec)
	next, err := ctrl.SelectCandidate("general")
	if err != nil || next.Spec.ID != event2.ID {
		t.Fatalf("second event after release=%v err=%v", next, err)
	}
}
