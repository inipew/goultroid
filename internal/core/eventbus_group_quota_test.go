package core

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type p7kEventTicket struct {
	id   tasks.TaskID
	done chan struct{}
	res  tasks.TaskResult
}

func (t *p7kEventTicket) TaskID() tasks.TaskID                           { return t.id }
func (t *p7kEventTicket) State() tasks.TaskState                         { return tasks.StateCompleted }
func (t *p7kEventTicket) Done() <-chan struct{}                          { return t.done }
func (t *p7kEventTicket) Result() (tasks.TaskResult, bool)               { return t.res, true }
func (t *p7kEventTicket) Wait(context.Context) (tasks.TaskResult, error) { return t.res, nil }

type p7kEventTaskClient struct {
	specs chan tasks.WorkSpec
}

func (c *p7kEventTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs <- spec
	res := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if spec.Handler != nil {
		if err := spec.Handler(ctx); err != nil {
			res.Outcome = tasks.OutcomeFailed
			res.Failure.Message = err.Error()
		}
	}
	if spec.OnComplete != nil {
		spec.OnComplete(res)
	}
	done := make(chan struct{})
	close(done)
	return &p7kEventTicket{id: spec.ID, done: done, res: res}, nil
}

func (*p7kEventTaskClient) Cancel(id tasks.TaskID, cause tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{TaskID: id, Accepted: true, Reason: cause}, nil
}
func (*p7kEventTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*p7kEventTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestP7KGroupServiceEventUsesChatQuotaOwner(t *testing.T) {
	client := &p7kEventTaskClient{specs: make(chan tasks.WorkSpec, 1)}
	bus := NewEventBus()
	bus.SetTasks(client)
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	sub := bus.SubscribeWithOptions(
		EventTypeGroupService,
		func(context.Context, Event) error { return nil },
		SubscribeOptions{Owner: "assistant:groupevents", Timeout: time.Second},
	)
	if sub == nil {
		t.Fatal("group service subscription was not created")
	}
	defer sub.Close()

	bus.Publish(&GroupServiceEvent{
		At:     time.Now(),
		ChatID: 77,
		Kind:   GroupServiceMemberJoined,
		Users:  []GroupServiceUser{{ID: 42}},
	})

	select {
	case spec := <-client.specs:
		if spec.QuotaOwner != tasks.OwnerID("telegram:chat:77") {
			t.Fatalf("quota owner=%q want telegram:chat:77", spec.QuotaOwner)
		}
		if spec.OrderingKey != "chat:77" {
			t.Fatalf("ordering key=%q want chat:77", spec.OrderingKey)
		}
		if spec.QueueDeadline.IsZero() || !spec.QueueDeadline.After(time.Now()) {
			t.Fatalf("queue deadline=%v want future deadline", spec.QueueDeadline)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for group event task")
	}
}
