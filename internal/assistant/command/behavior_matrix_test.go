package command_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type assistantBehaviorTaskClient struct {
	inner tasks.Client

	mu        sync.Mutex
	submits   int
	resources []tasks.ResourceRequirement
	scope     tasks.ScopeIdentity
}

func (c *assistantBehaviorTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.submits++
	c.resources = append([]tasks.ResourceRequirement(nil), spec.Resources...)
	c.scope = spec.Scope
	c.mu.Unlock()
	return c.inner.Submit(ctx, spec)
}

func (c *assistantBehaviorTaskClient) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	return c.inner.Cancel(id, reason)
}
func (c *assistantBehaviorTaskClient) CancelScope(scope tasks.ScopeIdentity, reason tasks.Cause) int {
	return c.inner.CancelScope(scope, reason)
}
func (c *assistantBehaviorTaskClient) Snapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return c.inner.Snapshot(id)
}

func (c *assistantBehaviorTaskClient) snapshot() (int, []tasks.ResourceRequirement, tasks.ScopeIdentity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.submits, append([]tasks.ResourceRequirement(nil), c.resources...), c.scope
}

func TestAssistantCanonicalBehaviorMatrix(t *testing.T) {
	const ownerID int64 = 100
	const regularID int64 = 300

	addonScope := tasks.ScopeIdentity{Owner: "addon:test", Generation: 7}

	cfg := taskengine.Config{
		Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
			"interactive": {Concurrency: 1, MinConcurrency: 1, BacklogLimit: 8},
		},
		ResultCapacity:     16,
		ResourceCapacities: map[string]int64{"process": 1},
	}
	engine := taskengine.NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()

	client := &assistantBehaviorTaskClient{inner: engine}
	router := command.NewRouter(zap.NewNop())
	router.SetOwner(ownerID, nil)
	router.SetTasks(client)

	var resourceCalls atomic.Int32
	var invocationDeniedCalls atomic.Int32
	var permissionDeniedCalls atomic.Int32

	coreRouter := core.NewRouter(".")
	if err := coreRouter.RegisterBatch([]core.Command{
		{
			Name:       "resource",
			Permission: core.PermissionEveryone,
			Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
			Surfaces:   execution.SurfaceAssistant,
			Resources:  []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
			Scope:      addonScope,
			Handler: func(*core.Context) error {
				resourceCalls.Add(1)
				return nil
			},
		},
		{
			Name:       "selfonly",
			Permission: core.PermissionEveryone,
			Invocation: core.InvocationPolicy{Assistant: core.InvocationSelfOnly},
			Surfaces:   execution.SurfaceAssistant,
			Resources:  []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
			Handler: func(*core.Context) error {
				invocationDeniedCalls.Add(1)
				return nil
			},
		},
		{
			Name:       "owneronly",
			Permission: core.PermissionOwner,
			Invocation: core.InvocationPolicy{Assistant: core.InvocationAnyone},
			Surfaces:   execution.SurfaceAssistant,
			Resources:  []tasks.ResourceRequirement{{Name: "process", Amount: 1}},
			Handler: func(*core.Context) error {
				permissionDeniedCalls.Add(1)
				return nil
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	router.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	peer := &tg.InputPeerUser{UserID: regularID}

	if err := router.Dispatch(context.Background(), regularID, peer, "/selfonly", fake); err != nil {
		t.Fatalf("selfonly dispatch: %v", err)
	}
	if submits, _, _ := client.snapshot(); submits != 0 {
		t.Fatalf("invocation-denied command task submissions=%d, want 0", submits)
	}
	if invocationDeniedCalls.Load() != 0 {
		t.Fatal("invocation-denied handler executed")
	}

	if err := router.Dispatch(context.Background(), regularID, peer, "/owneronly", fake); err != nil {
		t.Fatalf("owneronly dispatch: %v", err)
	}
	if submits, _, _ := client.snapshot(); submits != 0 {
		t.Fatalf("permission-denied command task submissions=%d, want 0", submits)
	}
	if permissionDeniedCalls.Load() != 0 {
		t.Fatal("permission-denied handler executed")
	}

	if err := router.Dispatch(context.Background(), regularID, peer, "/resource", fake); err != nil {
		t.Fatalf("resource dispatch: %v", err)
	}
	submits, resources, scope := client.snapshot()
	if submits != 1 {
		t.Fatalf("resource command task submissions=%d, want 1", submits)
	}
	if resourceCalls.Load() != 1 {
		t.Fatalf("resource handler calls=%d, want 1", resourceCalls.Load())
	}
	want := []tasks.ResourceRequirement{{Name: "process", Amount: 1}}
	if len(resources) != 1 || resources[0] != want[0] {
		t.Fatalf("task resources=%+v, want %+v", resources, want)
	}
	if scope != addonScope {
		t.Fatalf("task scope=%+v, want %+v", scope, addonScope)
	}
}
