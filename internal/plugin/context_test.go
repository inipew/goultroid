package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/platform/process"
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
