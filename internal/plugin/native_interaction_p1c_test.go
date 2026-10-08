package plugin

import (
	"context"
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
)

type p1cTaskClient struct {
	mu           sync.Mutex
	cancelScopes []tasks.ScopeIdentity
}

func (c *p1cTaskClient) Submit(context.Context, tasks.WorkSpec) (tasks.Ticket, error) {
	return nil, nil
}
func (c *p1cTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (c *p1cTaskClient) CancelScope(scope tasks.ScopeIdentity, _ tasks.Cause) int {
	c.mu.Lock()
	c.cancelScopes = append(c.cancelScopes, scope)
	c.mu.Unlock()
	return 0
}
func (c *p1cTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type p1cNativeDriverPlugin struct {
	binds    int
	cleanups int
	scopes   []tasks.ScopeIdentity
}

func (*p1cNativeDriverPlugin) Name() string             { return "p1c-native" }
func (*p1cNativeDriverPlugin) Commands() []core.Command { return nil }
func (*p1cNativeDriverPlugin) Init() error              { return nil }
func (*p1cNativeDriverPlugin) NativeFeatureID() string  { return "p1c-native" }

func (*p1cNativeDriverPlugin) FeatureSpec() feature.Spec {
	return feature.Spec{
		ID:   "p1c-native",
		Name: "P1C Native",
		Interactions: []feature.Interaction{{
			ID:       "run",
			Kind:     feature.InteractionAction,
			Surfaces: execution.SurfaceUserbot,
			Policy:   feature.OwnerPolicy(execution.SurfaceUserbot),
		}},
	}
}

func (p *p1cNativeDriverPlugin) BindNative(rt nativeinteraction.DriverRuntime) (func(), error) {
	p.binds++
	p.scopes = append(p.scopes, rt.Scope)
	registration, err := rt.Interactions.RegisterAction(rt.Scope, p.Name(), "run", func(*orchestration.Context) error {
		return nil
	})
	if err != nil {
		return nil, err
	}
	return func() {
		p.cleanups++
		registration.Close()
	}, nil
}

func TestP1CNativeDriverRebindsPerPluginGeneration(t *testing.T) {
	manager := NewManager(core.NewRouter("."))
	tasksClient := &p1cTaskClient{}
	manager.SetTaskClient(tasksClient)
	service := &core.MockTelegramServicer{}
	adapter, err := nativeinteraction.New(
		manager.FeatureCatalog(),
		manager.InteractionRuntime(),
		manager.ActionDispatcher(),
		tasksClient,
		func() presentationtelegram.BridgeService { return service },
		core.NewPermissions(1, nil),
	)
	if err != nil {
		t.Fatalf("nativeinteraction.New() error = %v", err)
	}
	manager.SetNativeInteractions(adapter)

	p := &p1cNativeDriverPlugin{}
	if err := manager.RegisterWithContext(context.Background(), p); err != nil {
		t.Fatalf("RegisterWithContext() error = %v", err)
	}
	if p.binds != 1 || p.cleanups != 0 || len(p.scopes) != 1 || p.scopes[0].IsZero() {
		t.Fatalf("initial lifecycle binds=%d cleanups=%d scopes=%v", p.binds, p.cleanups, p.scopes)
	}
	firstScope := p.scopes[0]

	if err := manager.Disable(context.Background(), p.Name()); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if p.cleanups != 1 {
		t.Fatalf("cleanup count after disable = %d, want 1", p.cleanups)
	}

	if err := manager.Enable(context.Background(), p.Name()); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if p.binds != 2 || len(p.scopes) != 2 || p.scopes[1].IsZero() || p.scopes[1] == firstScope {
		t.Fatalf("reload lifecycle binds=%d scopes=%v first=%v", p.binds, p.scopes, firstScope)
	}

	if err := manager.ShutdownWithContext(context.Background()); err != nil {
		t.Fatalf("ShutdownWithContext() error = %v", err)
	}
	if p.cleanups != 2 {
		t.Fatalf("cleanup count after shutdown = %d, want 2", p.cleanups)
	}
}

type p1cOptionalNativeDriverPlugin struct {
	p1cNativeDriverPlugin
}

func (*p1cOptionalNativeDriverPlugin) NativeOptional() bool { return true }

func TestA5NativeDriverStandaloneFallbackDoesNotRelaxStrictDrivers(t *testing.T) {
	ctx := context.Background()
	strict := NewManager(core.NewRouter("."))
	if err := strict.RegisterWithContext(ctx, &p1cNativeDriverPlugin{}); err == nil {
		t.Fatal("strict native driver registered without an a2 adapter")
	}
	optionalManager := NewManager(core.NewRouter("."))
	fallback := &p1cOptionalNativeDriverPlugin{}
	if err := optionalManager.RegisterWithContext(ctx, fallback); err != nil {
		t.Fatalf("optional native driver rejected its text-only standalone path: %v", err)
	}
	if fallback.binds != 0 {
		t.Fatal("optional driver unexpectedly bound a native action without adapter")
	}
	if err := optionalManager.Disable(ctx, fallback.Name()); err != nil {
		t.Fatalf("optional driver cleanup failed: %v", err)
	}
}
