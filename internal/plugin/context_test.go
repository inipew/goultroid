package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/platform/process"
	"github.com/inipew/goultroid/internal/platform/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestPluginContext_CapabilityEnforcement(t *testing.T) {
	gate := NewCapabilityGate()

	// Register manifest with only network.http
	_ = gate.RegisterManifest(Manifest{
		ID:           "weather",
		Name:         "Weather",
		Version:      "1.0.0",
		Capabilities: []string{CapHTTP},
	})

	netSvc := network.NewService(nil, nil)
	procMgr := process.NewManager(nil, 0, nil)

	ctx := NewPluginContext(context.Background(), ContextConfig{
		Owner:   "weather",
		Gate:    gate,
		Network: netSvc,
		Process: procMgr,
	})

	// HTTP should succeed because it is in manifest
	httpSvc, err := ctx.HTTP()
	if err != nil {
		t.Fatalf("unexpected HTTP error: %v", err)
	}
	if httpSvc == nil {
		t.Fatalf("expected non-nil HTTP service")
	}

	// Process execution should be denied because it is privileged and not in manifest or allowlist
	_, err = ctx.Process()
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("expected ErrCapabilityDenied for Process, got %v", err)
	}

	// Now allow process execution for weather
	gate.AllowPrivileged("weather", CapProcessExecute)
	_ = gate.RegisterManifest(Manifest{
		ID:           "weather",
		Name:         "Weather",
		Version:      "1.0.0",
		Capabilities: []string{CapHTTP, CapProcessExecute},
	})

	proc, err := ctx.Process()
	if err != nil {
		t.Fatalf("expected Process to succeed after grant, got %v", err)
	}
	if proc == nil {
		t.Fatalf("expected non-nil process manager")
	}
}

func TestPluginContext_StorageCapability(t *testing.T) {
	gate := NewCapabilityGate()
	storageMgr := storage.NewManager(nil) // memory fallback

	_ = gate.RegisterManifest(Manifest{
		ID:           "test_plugin",
		Name:         "Test Plugin",
		Version:      "1.0.0",
		Capabilities: []string{CapStorageRead},
	})

	ctx := NewPluginContext(context.Background(), ContextConfig{
		Owner:   "test_plugin",
		Gate:    gate,
		Storage: storageMgr,
	})

	// Storage should succeed because CapStorageRead is granted
	store, err := ctx.Storage()
	if err != nil {
		t.Fatalf("expected Storage to succeed, got %v", err)
	}

	// Should be read-only
	err = store.Set(context.Background(), "k", []byte("v"))
	if !errors.Is(err, storage.ErrReadOnly) {
		t.Fatalf("expected ErrReadOnly, got %v", err)
	}

	// Now register with write as well
	_ = gate.RegisterManifest(Manifest{
		ID:           "test_plugin",
		Name:         "Test Plugin",
		Version:      "1.0.0",
		Capabilities: []string{CapStorageRead, CapStorageWrite},
	})

	store2, err := ctx.Storage()
	if err != nil {
		t.Fatalf("expected Storage to succeed, got %v", err)
	}
	if err := store2.Set(context.Background(), "k", []byte("hello")); err != nil {
		t.Fatalf("expected Set to succeed with CapStorageWrite, got %v", err)
	}
	val, err := store2.Get(context.Background(), "k")
	if err != nil || string(val) != "hello" {
		t.Fatalf("expected hello, got %s (err: %v)", string(val), err)
	}

	// An unprivileged plugin with no storage capabilities
	_ = gate.RegisterManifest(Manifest{
		ID:           "unprivileged",
		Name:         "Unprivileged",
		Version:      "1.0.0",
		Capabilities: []string{},
	})
	ctxUnpriv := NewPluginContext(context.Background(), ContextConfig{
		Owner:   "unprivileged",
		Gate:    gate,
		Storage: storageMgr,
	})
	_, err = ctxUnpriv.Storage()
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("expected ErrCapabilityDenied for unprivileged plugin, got %v", err)
	}
}

type dummyTaskClient struct {
	tasks.Client
}

func TestPluginContext_TaskClientCapability(t *testing.T) {
	gate := NewCapabilityGate()
	_ = gate.RegisterManifest(Manifest{
		ID:           "worker_plugin",
		Name:         "Worker Plugin",
		Version:      "1.0.0",
		Capabilities: []string{CapTasks},
	})

	client := &dummyTaskClient{}
	ctx := NewPluginContext(context.Background(), ContextConfig{
		Owner:      "worker_plugin",
		Scope:      NewScope(context.Background(), "worker_plugin"),
		Gate:       gate,
		TaskClient: client,
	})

	tc, err := ctx.TaskClient()
	if err != nil {
		t.Fatalf("expected TaskClient to succeed, got %v", err)
	}
	if tc == client {
		t.Fatal("expected plugin-scoped task client")
	}

	// Without CapTasks
	_ = gate.RegisterManifest(Manifest{
		ID:           "no_tasks",
		Name:         "No Tasks",
		Version:      "1.0.0",
		Capabilities: []string{},
	})
	ctxDenied := NewPluginContext(context.Background(), ContextConfig{
		Owner:      "no_tasks",
		Scope:      NewScope(context.Background(), "no_tasks"),
		Gate:       gate,
		TaskClient: client,
	})
	_, err = ctxDenied.TaskClient()
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("expected ErrCapabilityDenied, got %v", err)
	}
}

type mockTaskClient struct {
	tasks.Client
	snapshots map[tasks.TaskID]tasks.TaskSnapshot
	cancelled map[tasks.TaskID]bool
}

func (m *mockTaskClient) Snapshot(id tasks.TaskID) (tasks.TaskSnapshot, bool) {
	s, ok := m.snapshots[id]
	return s, ok
}

func (m *mockTaskClient) Cancel(id tasks.TaskID, cause tasks.Cause) (tasks.CancelReceipt, error) {
	if m.cancelled == nil {
		m.cancelled = make(map[tasks.TaskID]bool)
	}
	m.cancelled[id] = true
	return tasks.CancelReceipt{TaskID: id, Accepted: true, Reason: cause}, nil
}

func TestPluginContext_ScopedTaskClient_ScopeIsolation(t *testing.T) {
	gate := NewCapabilityGate()
	_ = gate.RegisterManifest(Manifest{
		ID:           "plugin_a",
		Name:         "Plugin A",
		Version:      "1.0.0",
		Capabilities: []string{CapTasks},
	})

	scopeA := NewScope(context.Background(), "plugin_a")
	scopeB := NewScope(context.Background(), "plugin_b")

	client := &mockTaskClient{
		snapshots: map[tasks.TaskID]tasks.TaskSnapshot{
			"task_a": {
				ID:    "task_a",
				Scope: tasks.ScopeIdentity{Owner: "plugin:plugin_a", Generation: scopeA.Generation()},
			},
			"task_b": {
				ID:    "task_b",
				Scope: tasks.ScopeIdentity{Owner: "plugin:plugin_b", Generation: scopeB.Generation()},
			},
		},
	}

	ctx := NewPluginContext(context.Background(), ContextConfig{
		Owner:      "plugin_a",
		Scope:      scopeA,
		Gate:       gate,
		TaskClient: client,
	})

	tc, err := ctx.TaskClient()
	if err != nil {
		t.Fatalf("failed to get task client: %v", err)
	}

	// 1. Cancel own task (task_a) should succeed
	receipt, err := tc.Cancel("task_a", tasks.CauseUserCancel)
	if err != nil || !receipt.Accepted {
		t.Fatalf("expected cancel own task to succeed, got receipt=%+v, err=%v", receipt, err)
	}
	if !client.cancelled["task_a"] {
		t.Errorf("expected task_a to be recorded as cancelled in backend")
	}

	// 2. Cancel foreign task (task_b) should be rejected
	receiptB, errB := tc.Cancel("task_b", tasks.CauseUserCancel)
	if !errors.Is(errB, tasks.ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound when cancelling foreign task, got receipt=%+v, err=%v", receiptB, errB)
	}
	if client.cancelled["task_b"] {
		t.Errorf("foreign task_b must not be cancelled by plugin_a")
	}

	// 3. Snapshot foreign task should return false
	_, ok := tc.Snapshot("task_b")
	if ok {
		t.Errorf("foreign task snapshot must not be visible to plugin_a")
	}
}
