package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

func TestR2ExecutionPolicyControlsTaskAndHandlerBudgets(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	tasksClient := &r0RecordingTaskClient{runWork: true}
	d.SetTasks(tasksClient)

	deadlineObserved := make(chan time.Duration, 1)
	cleanup, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Scope:    tasks.ScopeIdentity{Owner: "plugin:r2-budget", Generation: 1},
		Priority: PriorityFeature,
		Routing: core.MessageHookRouting{
			Lane: core.MessageHookEvent,
			Interests: []core.MessageHookInterest{{
				Directions: core.MessageDirectionIncoming,
				Peers:      core.MessagePeerGroup,
			}},
		},
		Execution: core.MessageHookExecutionPolicy{
			FailurePolicy:  core.MessageHookFailOpen,
			HandlerTimeout: 150 * time.Millisecond,
			TaskTimeout:    2 * time.Second,
			Ordering:       core.MessageHookOrderingChat,
		},
		Handler: func(ctx context.Context, _ *core.MessageEnvelope) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("registered handler context has no deadline")
			}
			deadlineObserved <- time.Until(deadline)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	msg := &tg.Message{
		ID:      501,
		PeerID:  &tg.PeerChat{ChatID: 42},
		FromID:  &tg.PeerUser{UserID: 7},
		Message: "hello",
	}
	envelope := NormalizeMessageEnvelope(tg.Entities{}, msg, false, "", 1)
	_, events := d.messageHandlersForEnvelope(envelope)
	if len(events) != 1 {
		t.Fatalf("event handlers=%d, want 1", len(events))
	}
	d.dispatchEventHandlersEnvelope(context.Background(), events, envelope, tg.Entities{}, msg)

	select {
	case remaining := <-deadlineObserved:
		if remaining <= 0 || remaining > 150*time.Millisecond {
			t.Fatalf("handler deadline remaining=%s, want (0,150ms]", remaining)
		}
	case <-time.After(time.Second):
		t.Fatal("event handler did not run")
	}

	specs := tasksClient.snapshot()
	if len(specs) != 1 {
		t.Fatalf("TaskEngine submissions=%d, want 1", len(specs))
	}
	if specs[0].ExecutionTimeout != 2*time.Second {
		t.Fatalf("task timeout=%s, want 2s", specs[0].ExecutionTimeout)
	}
	if specs[0].OrderingKey != "msg-event:chat:42" {
		t.Fatalf("explicit chat ordering key=%q", specs[0].OrderingKey)
	}
}

func TestR2ExplicitFailurePolicyOverridesPriority(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	cleanup, err := d.RegisterMessageHook(core.MessageHookRegistration{
		Scope:    tasks.ScopeIdentity{Owner: "plugin:r2-failure", Generation: 1},
		Priority: PrioritySecurity,
		Routing: core.MessageHookRouting{
			Lane: core.MessageHookDecision,
			Interests: []core.MessageHookInterest{{
				Directions: core.MessageDirectionIncoming,
				Peers:      core.MessagePeerGroup,
			}},
		},
		Execution: core.MessageHookExecutionPolicy{
			FailurePolicy: core.MessageHookFailOpen,
		},
		Handler: func(context.Context, *core.MessageEnvelope) error {
			t.Fatal("handler ran without TaskEngine")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	msg := &tg.Message{
		ID:      502,
		PeerID:  &tg.PeerChat{ChatID: 42},
		FromID:  &tg.PeerUser{UserID: 7},
		Message: "hello",
	}
	handlers, _ := d.messageHandlersFor(msg, false)
	if len(handlers) != 1 {
		t.Fatalf("decision handlers=%d, want 1", len(handlers))
	}
	if policy := messageHookExecutionPolicy(handlers[0]); policy.FailurePolicy != core.MessageHookFailOpen {
		t.Fatalf("explicit failure policy=%v, want fail-open", policy.FailurePolicy)
	}
	if handled := d.executeDecisionHandlers(
		context.Background(),
		handlers,
		tg.Entities{},
		msg,
		false,
		"",
	); handled {
		t.Fatal("security priority overrode explicit fail-open execution policy")
	}
}
