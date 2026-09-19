package telegram

import (
	"context"
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type scopeCaptureTaskClient struct {
	mu   sync.Mutex
	spec tasks.WorkSpec
}

func (c *scopeCaptureTaskClient) Submit(_ context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.spec = spec
	c.mu.Unlock()
	if spec.OnComplete != nil {
		spec.OnComplete(tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted})
	}
	return nil, nil
}
func (*scopeCaptureTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*scopeCaptureTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*scopeCaptureTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}
func (c *scopeCaptureTaskClient) captured() tasks.WorkSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.spec
}

func TestSubmitInteractiveCommandPreservesCommandScope(t *testing.T) {
	dispatcher := NewDispatcher(core.NewRouter("."), core.NewPermissions(100, nil), nil, zap.NewNop())
	client := &scopeCaptureTaskClient{}
	dispatcher.SetTasks(client)

	scope := tasks.ScopeIdentity{Owner: "addon:sample", Generation: 9}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := core.Command{Name: "sample", Scope: scope}
	coreCtx := &core.Context{Ctx: ctx}

	if err := dispatcher.submitInteractiveCommand(ctx, cancel, coreCtx, cmd, "cmd:1:1", "telegram:user:100", "corr"); err != nil {
		t.Fatal(err)
	}
	if got := client.captured().Scope; got != scope {
		t.Fatalf("task scope=%+v, want %+v", got, scope)
	}
}
