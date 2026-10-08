package telegram

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type a2CaptureTaskClient struct {
	decisionPolicyTaskClient
	submitted []tasks.WorkSpec
}

func (c *a2CaptureTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.submitted = append(c.submitted, spec)
	return c.decisionPolicyTaskClient.Submit(ctx, spec)
}

func TestA2DispatcherUsesIsolatedOrderingDomains(t *testing.T) {
	dispatcher := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	client := &a2CaptureTaskClient{decisionPolicyTaskClient: decisionPolicyTaskClient{run: true}}
	dispatcher.SetTasks(client)

	message := &tg.Message{ID: 42, PeerID: &tg.PeerChat{ChatID: 7}, Message: "hello"}
	envelope := NormalizeMessageEnvelope(tg.Entities{}, message, false, "", 1)
	decision := prioritizedHandler{
		id: 1, priority: PriorityFeature, routing: core.MessageHookRouting{Lane: core.MessageHookDecision},
		scope:            tasks.ScopeIdentity{Owner: "plugin:afk", Generation: 1},
		canonicalHandler: func(context.Context, *core.MessageEnvelope) error { return nil },
	}
	event := prioritizedHandler{
		id: 2, priority: PriorityObservability, routing: core.MessageHookRouting{Lane: core.MessageHookEvent},
		scope:            tasks.ScopeIdentity{Owner: "plugin:userlog", Generation: 1},
		canonicalHandler: func(context.Context, *core.MessageEnvelope) error { return nil },
	}
	if dispatcher.executeDecisionHandlersEnvelope(context.Background(), []prioritizedHandler{decision}, envelope, tg.Entities{}, message) {
		t.Fatal("no-op decision unexpectedly handled message")
	}
	dispatcher.dispatchEventHandlersEnvelope(context.Background(), []prioritizedHandler{event}, envelope, tg.Entities{}, message)
	if len(client.submitted) != 2 {
		t.Fatalf("tasks=%d, want decision and event", len(client.submitted))
	}
	if got := client.submitted[0].OrderingKey; got != "msg-decision:chat:7" {
		t.Fatalf("actual dispatcher decision key=%q", got)
	}
	if got := client.submitted[1].OrderingKey; got != "msg-event:plugin:userlog:chat:7" {
		t.Fatalf("actual dispatcher event key=%q", got)
	}
	if client.submitted[0].OrderingKey == client.submitted[1].OrderingKey {
		t.Fatal("decision and event still share ordering domain")
	}
}
