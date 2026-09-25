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


func TestP5UserbotCommandAdmissionKeepsResourceProfile(t *testing.T) {
	dispatcher := NewDispatcher(core.NewRouter("."), core.NewPermissions(100, nil), nil, zap.NewNop())
	client := &scopeCaptureTaskClient{}
	dispatcher.SetTasks(client)

	scope := tasks.ScopeIdentity{Owner: "plugin:p5", Generation: 11}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := core.Command{
		Name:  "p5",
		Scope: scope,
		Resources: []tasks.ResourceRequirement{
			{Name: "download", Amount: 1},
			{Name: "process", Amount: 1},
		},
		Handler: func(*core.Context) error { return nil },
	}
	coreCtx := &core.Context{Ctx: ctx}

	if err := dispatcher.submitInteractiveCommand(ctx, cancel, coreCtx, cmd, "cmd:p5:1", "telegram:user:100", "p5-correlation"); err != nil {
		t.Fatalf("submitInteractiveCommand() error=%v", err)
	}
	spec := client.captured()
	if spec.Scope != scope {
		t.Fatalf("P5 command scope=%+v, want %+v", spec.Scope, scope)
	}
	if spec.Pool != tasks.PoolID("interactive") || spec.Class != tasks.PriorityInteractive {
		t.Fatalf("P5 command admission pool/class=%q/%q", spec.Pool, spec.Class)
	}
	if len(spec.Resources) != 2 ||
		spec.Resources[0].Name != "download" || spec.Resources[0].Amount != 1 ||
		spec.Resources[1].Name != "process" || spec.Resources[1].Amount != 1 {
		t.Fatalf("P5 command resources=%+v", spec.Resources)
	}
	if spec.OrderingKey != "p5-correlation" {
		t.Fatalf("P5 command ordering key=%q", spec.OrderingKey)
	}
}
