package core

import (
	"testing"
	"time"
)

func TestNormalizeMessageHookExecutionPolicyDefaults(t *testing.T) {
	decision, err := NormalizeMessageHookExecutionPolicy(
		10,
		MessageHookDecision,
		MessageHookExecutionPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision.FailurePolicy != MessageHookFailClosed ||
		decision.HandlerTimeout != 5*time.Second ||
		decision.TaskTimeout != 5*time.Second ||
		decision.Ordering != MessageHookOrderingChat {
		t.Fatalf("decision defaults=%+v", decision)
	}

	event, err := NormalizeMessageHookExecutionPolicy(
		50,
		MessageHookEvent,
		MessageHookExecutionPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if event.FailurePolicy != MessageHookFailOpen ||
		event.HandlerTimeout != 5*time.Second ||
		event.TaskTimeout != 10*time.Second ||
		event.Ordering != MessageHookOrderingPluginChat {
		t.Fatalf("event defaults=%+v", event)
	}
}

func TestNormalizeMessageHookExecutionPolicyPreservesExplicitFailurePolicy(t *testing.T) {
	policy, err := NormalizeMessageHookExecutionPolicy(
		10,
		MessageHookDecision,
		MessageHookExecutionPolicy{FailurePolicy: MessageHookFailOpen},
	)
	if err != nil {
		t.Fatal(err)
	}
	if policy.FailurePolicy != MessageHookFailOpen {
		t.Fatalf("explicit failure policy=%v, want fail-open", policy.FailurePolicy)
	}
}

func TestNormalizeMessageHookExecutionPolicyRejectsInvalidBudgets(t *testing.T) {
	_, err := NormalizeMessageHookExecutionPolicy(
		50,
		MessageHookEvent,
		MessageHookExecutionPolicy{
			HandlerTimeout: 6 * time.Second,
			TaskTimeout:    5 * time.Second,
		},
	)
	if err == nil {
		t.Fatal("expected handler timeout above task timeout to fail")
	}

	_, err = NormalizeMessageHookExecutionPolicy(
		50,
		MessageHookEvent,
		MessageHookExecutionPolicy{TaskTimeout: -time.Second},
	)
	if err == nil {
		t.Fatal("expected negative task timeout to fail")
	}
}


func TestNormalizeMessageHookExecutionPolicyPreservesPluginGlobalOrdering(t *testing.T) {
	policy, err := NormalizeMessageHookExecutionPolicy(
		50,
		MessageHookDecision,
		MessageHookExecutionPolicy{Ordering: MessageHookOrderingPlugin},
	)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Ordering != MessageHookOrderingPlugin {
		t.Fatalf("ordering=%v, want plugin-global", policy.Ordering)
	}
}
