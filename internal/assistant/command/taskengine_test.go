package command_test

import (
	"context"
	"errors"
	"testing"

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
