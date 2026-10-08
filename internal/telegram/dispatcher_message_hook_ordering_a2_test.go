package telegram

import (
	"testing"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestA2MessageHookOrderingDomains(t *testing.T) {
	chat := core.PeerRef{Kind: core.PeerKindChat, ID: 42}
	decision := messageHookDecisionOrderingKey(chat)
	if decision != "msg-decision:chat:42" {
		t.Fatalf("decision key=%q", decision)
	}
	afk := tasks.ScopeIdentity{Owner: "plugin:afk", Generation: 1}
	log := tasks.ScopeIdentity{Owner: "plugin:userlog", Generation: 1}
	event := messageHookEventOrderingKey(afk, chat)
	if event != "msg-event:plugin:afk:chat:42" {
		t.Fatalf("AFK event key=%q", event)
	}
	if event == decision || event == messageHookEventOrderingKey(log, chat) {
		t.Fatal("decision/event or separate plugin event ordering domains collide")
	}
	if event != messageHookEventOrderingKey(tasks.ScopeIdentity{Owner: afk.Owner, Generation: 2}, chat) {
		t.Fatal("event ordering must be stable across plugin generation changes")
	}
	if event == messageHookEventOrderingKey(afk, core.PeerRef{Kind: core.PeerKindChat, ID: 43}) {
		t.Fatal("different chats must have different event ordering keys")
	}
	if key := messageHookEventOrderingKey(tasks.ScopeIdentity{}, chat); key != "msg-event:unscoped:chat:42" {
		t.Fatalf("unscoped key=%q", key)
	}
}

func TestA2StalledEventDoesNotBlockDecisionAdmission(t *testing.T) {
	ctrl := admission.NewController(map[tasks.PoolID]admission.PoolConfig{
		"interactive": {BacklogLimit: 8, PayloadBudget: 1 << 20},
		"general":     {BacklogLimit: 8, PayloadBudget: 1 << 20},
	})
	peer := core.PeerRef{Kind: core.PeerKindChat, ID: 42}
	eventKey := messageHookEventOrderingKey(tasks.ScopeIdentity{Owner: "plugin:userlog", Generation: 1}, peer)
	decisionKey := messageHookDecisionOrderingKey(peer)
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

func TestMessageHookPeerIdentitySeparatesTaskAndOrderingNamespaces(t *testing.T) {
	peers := []struct {
		peer core.PeerRef
		want string
	}{
		{core.PeerRef{Kind: core.PeerKindUser, ID: 42}, "user:42"},
		{core.PeerRef{Kind: core.PeerKindChat, ID: 42}, "chat:42"},
		{core.PeerRef{Kind: core.PeerKindChannel, ID: 42}, "channel:42"},
	}
	seen := make(map[string]bool)
	for _, tc := range peers {
		if got := messageHookPeerIdentity(tc.peer); got != tc.want {
			t.Fatalf("peer identity=%q; want %q", got, tc.want)
		}
		scope := tasks.ScopeIdentity{Owner: "plugin:afk", Generation: 1}
		for _, key := range []string{
			string(messageHookTaskID("decision", 7, tc.peer, 101)),
			string(messageHookTaskID("hook", 7, tc.peer, 101)),
			messageHookDecisionOrderingKey(tc.peer),
			messageHookEventOrderingKey(scope, tc.peer),
		} {
			if seen[key] {
				t.Fatalf("peer namespaces collided on %q", key)
			}
			seen[key] = true
		}
		if messageHookPeerIdentity(core.PeerRef{Kind: tc.peer.Kind, ID: 42, AccessHash: 555}) != tc.want {
			t.Fatal("access hash must not change stable peer identity")
		}
	}
	if messageHookPeerIdentity(core.PeerRef{}) != "unknown:0" {
		t.Fatal("empty peer must be classified as unknown")
	}
}
