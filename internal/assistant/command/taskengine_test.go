package command_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type inlineTicket struct {
	id   tasks.TaskID
	res  tasks.TaskResult
	done chan struct{}
}

func (t *inlineTicket) TaskID() tasks.TaskID                           { return t.id }
func (t *inlineTicket) State() tasks.TaskState                         { return tasks.StateCompleted }
func (t *inlineTicket) Done() <-chan struct{}                          { return t.done }
func (t *inlineTicket) Result() (tasks.TaskResult, bool)               { return t.res, true }
func (t *inlineTicket) Wait(context.Context) (tasks.TaskResult, error) { return t.res, nil }

type inlineTaskClient struct {
	last tasks.WorkSpec
}

func (c *inlineTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.last = spec
	res := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if err := spec.Handler(ctx); err != nil {
		res.Outcome = tasks.OutcomeFailed
		res.Failure.Message = err.Error()
	}
	done := make(chan struct{})
	close(done)
	return &inlineTicket{id: spec.ID, res: res, done: done}, nil
}

func (c *inlineTaskClient) Cancel(id tasks.TaskID, cause tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{TaskID: id, Accepted: true, Reason: cause}, nil
}
func (c *inlineTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *inlineTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestCommandRouter_ResourceCommandUsesTaskClient(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	client := &inlineTaskClient{}
	r.SetTasks(client)

	called := false
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:      "mediaheavy",
		Surfaces:  execution.SurfaceAssistant,
		Resources: []tasks.ResourceRequirement{{Name: "process", Amount: 1}, {Name: "media", Amount: 1}},
		Handler: func(*core.Context) error {
			called = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	if err := r.Dispatch(context.Background(), 42, &tg.InputPeerUser{UserID: 42}, "/mediaheavy", &fakeInteraction{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected command handler to execute through task client")
	}
	if client.last.Pool != "interactive" || client.last.Class != tasks.PriorityInteractive {
		t.Fatalf("unexpected execution class: pool=%s class=%s", client.last.Pool, client.last.Class)
	}
	if client.last.QuotaOwner != "assistant:user:42" {
		t.Fatalf("unexpected quota owner: %s", client.last.QuotaOwner)
	}
	if len(client.last.Resources) != 2 || client.last.Resources[0].Name != "process" || client.last.Resources[1].Name != "media" {
		t.Fatalf("resource requirements were not preserved: %+v", client.last.Resources)
	}
}

func TestCommandRouter_ResourceCommandFailsClosedWithoutTaskClient(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:      "resourceheavy",
		Surfaces:  execution.SurfaceAssistant,
		Resources: []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
		Handler:   func(*core.Context) error { return errors.New("must not run") },
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	err := r.Dispatch(context.Background(), 7, &tg.InputPeerUser{UserID: 7}, "/resourceheavy", &fakeInteraction{})
	if !errors.Is(err, command.ErrTasksNotConfigured) {
		t.Fatalf("expected ErrTasksNotConfigured, got %v", err)
	}
}

func TestCommandRouter_P7JGroupTaskOrderingIsTopicScoped(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	client := &inlineTaskClient{}
	r.SetTasks(client)

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:       "topicprobe",
		Surfaces:   execution.SurfaceAssistant,
		GroupOnly:  true,
		Permission: core.PermissionEveryone,
		Handler:    func(*core.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)
	peer := &tg.InputPeerChannel{ChannelID: 99, AccessHash: 123}

	for _, tc := range []struct {
		topic int
		want  string
	}{
		{topic: 7, want: "chat:99:topic:7"},
		{topic: 8, want: "chat:99:topic:8"},
		{topic: 0, want: "chat:99"},
	} {
		err := r.DispatchMessageContext(
			context.Background(),
			42,
			peer,
			"/topicprobe",
			command.MessageContext{
				Chat:      core.Chat{ID: 99, Type: "supergroup", AccessHash: 123},
				MessageID: 10 + tc.topic,
				TopicID:   tc.topic,
			},
			&fakeInteraction{},
		)
		if err != nil {
			t.Fatalf("topic %d dispatch: %v", tc.topic, err)
		}
		if client.last.OrderingKey != tc.want {
			t.Fatalf("topic %d ordering=%q want %q", tc.topic, client.last.OrderingKey, tc.want)
		}
		if client.last.QuotaOwner != "telegram:chat:99" {
			t.Fatalf("topic %d quota owner=%q want telegram:chat:99", tc.topic, client.last.QuotaOwner)
		}
		if client.last.QueueDeadline.IsZero() || !client.last.QueueDeadline.After(time.Now()) {
			t.Fatalf("topic %d queue deadline=%v want future deadline", tc.topic, client.last.QueueDeadline)
		}
		if client.last.ExecutionTimeout != 30*time.Second {
			t.Fatalf("topic %d execution timeout=%v want 30s", tc.topic, client.last.ExecutionTimeout)
		}
	}
}
