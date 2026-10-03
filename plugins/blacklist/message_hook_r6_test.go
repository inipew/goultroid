package blacklist

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/telegram"
)

type r6Ticket struct {
	id tasks.TaskID
}

func (t *r6Ticket) TaskID() tasks.TaskID { return t.id }
func (*r6Ticket) State() tasks.TaskState { return tasks.StateQueued }
func (*r6Ticket) Done() <-chan struct{}  { return make(chan struct{}) }
func (*r6Ticket) Result() (tasks.TaskResult, bool) {
	return tasks.TaskResult{}, false
}
func (*r6Ticket) Wait(context.Context) (tasks.TaskResult, error) {
	return tasks.TaskResult{}, nil
}

type r6TaskClient struct {
	specs []tasks.WorkSpec
}

func (c *r6TaskClient) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs = append(c.specs, spec)
	return &r6Ticket{id: spec.ID}, nil
}

func (*r6TaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}

func (*r6TaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }

func (*r6TaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func TestR6BlacklistDecisionAdmitsDeleteEffectWithoutWaitingForRPC(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db)
	const chatID int64 = 42
	if err := repo.AddBlacklist(context.Background(), chatID, "spam"); err != nil {
		t.Fatal(err)
	}

	svc := &mockService{}
	p := New(repo, func() core.TelegramServicer { return svc })
	if err := p.InitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	taskClient := &r6TaskClient{}
	p.taskMu.Lock()
	p.tasks = taskClient
	p.taskMu.Unlock()

	registrations := p.MessageHookRegistrations()
	if len(registrations) != 1 {
		t.Fatalf("registrations=%d, want 1", len(registrations))
	}
	registration := registrations[0]
	if registration.Routing.Lane != core.MessageHookDecision {
		t.Fatalf("lane=%v, want decision", registration.Routing.Lane)
	}
	if registration.Execution.FailurePolicy != core.MessageHookFailClosed {
		t.Fatalf("failure policy=%v, want fail-closed", registration.Execution.FailurePolicy)
	}
	if registration.Execution.Ordering != core.MessageHookOrderingChat {
		t.Fatalf("ordering=%v, want chat", registration.Execution.Ordering)
	}

	envelope := telegram.NormalizeMessageEnvelope(tg.Entities{}, &tg.Message{
		ID:      77,
		PeerID:  &tg.PeerChat{ChatID: chatID},
		FromID:  &tg.PeerUser{UserID: 100},
		Message: "contains spam keyword",
	}, false, "", 1)
	decision := core.NewMessageDecision(core.ExecutionInteractive)
	ctx := core.WithMessageDecision(context.Background(), decision)
	err = registration.Handler(ctx, envelope)
	if !errors.Is(err, core.ErrInterceptHandled) {
		t.Fatalf("decision error=%v, want ErrInterceptHandled", err)
	}
	if svc.deleteCalled {
		t.Fatal("delete RPC ran inside synchronous blacklist decision")
	}
	if !decision.IsHandled() || !decision.IsSuppressedFilters() || !decision.IsSuppressedCommands() {
		t.Fatal("blacklist match did not publish synchronous suppression decision")
	}
	if len(taskClient.specs) != 1 {
		t.Fatalf("delete effect submissions=%d, want 1", len(taskClient.specs))
	}
	spec := taskClient.specs[0]
	if spec.Pool != tasks.PoolID("general") {
		t.Fatalf("delete effect pool=%q, want general", spec.Pool)
	}
	if spec.OrderingKey != "blacklist-effect:chat:42" {
		t.Fatalf("delete effect ordering=%q", spec.OrderingKey)
	}
	if spec.ExecutionTimeout != blacklistDeleteEffectTimeout {
		t.Fatalf("delete effect timeout=%s, want %s", spec.ExecutionTimeout, blacklistDeleteEffectTimeout)
	}

	if err := spec.Handler(context.Background()); err != nil {
		t.Fatalf("delete effect execution: %v", err)
	}
	if !svc.deleteCalled {
		t.Fatal("delete effect did not execute Telegram deletion")
	}
	if len(svc.deletedMsgIDs) != 1 || svc.deletedMsgIDs[0] != 77 {
		t.Fatalf("deleted message ids=%v, want [77]", svc.deletedMsgIDs)
	}
}
