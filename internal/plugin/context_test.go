package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/platform/process"
	"github.com/inipew/goultroid/internal/platform/storage"
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
