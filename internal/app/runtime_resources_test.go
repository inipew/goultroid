package app

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/runtime"
)

type mockResourceComp struct {
	name       string
	deps       []string
	isCritical bool
	quiesced   bool
	drained    bool
	forceStop  bool
}

func (m *mockResourceComp) Name() string           { return m.name }
func (m *mockResourceComp) Dependencies() []string { return m.deps }
func (m *mockResourceComp) Start(context.Context) error {
	return nil
}
func (m *mockResourceComp) Stop(context.Context) error {
	return nil
}
func (m *mockResourceComp) Health(context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}
func (m *mockResourceComp) IsCritical() bool {
	return m.isCritical
}
func (m *mockResourceComp) Quiesce(context.Context) error {
	m.quiesced = true
	return nil
}
func (m *mockResourceComp) Drain(context.Context) error {
	m.drained = true
	return nil
}
func (m *mockResourceComp) ForceStop(context.Context) error {
	m.forceStop = true
	return nil
}

func TestDependencyComponent_ForwardsCriticalAndLifecycleInterfaces(t *testing.T) {
	optional := &mockResourceComp{name: "optional", deps: []string{"taskengine"}, isCritical: false}
	wrappedOptional := dependencyComponent{Component: optional, dependencies: []string{"db", "taskengine"}}

	if wrappedOptional.Name() != "optional" {
		t.Fatalf("name = %s, want optional", wrappedOptional.Name())
	}
	deps := wrappedOptional.Dependencies()
	if len(deps) != 2 || deps[0] != "db" || deps[1] != "taskengine" {
		t.Fatalf("deps = %+v, want [db, taskengine]", deps)
	}

	// Verify CriticalComponent forwarding
	if cc, ok := any(wrappedOptional).(runtime.CriticalComponent); !ok {
		t.Fatal("wrapped component does not expose runtime.CriticalComponent")
	} else if cc.IsCritical() {
		t.Fatal("wrapped optional component reported IsCritical() = true, want false")
	}

	critical := &mockResourceComp{name: "critical", isCritical: true}
	wrappedCritical := dependencyComponent{Component: critical}
	if cc, ok := any(wrappedCritical).(runtime.CriticalComponent); !ok {
		t.Fatal("wrapped critical component does not expose runtime.CriticalComponent")
	} else if !cc.IsCritical() {
		t.Fatal("wrapped critical component reported IsCritical() = false, want true")
	}

	// Verify Quiesce/Drain/ForceStop forwarding
	ctx := context.Background()
	_ = wrappedOptional.Quiesce(ctx)
	if !optional.quiesced {
		t.Fatal("Quiesce was not forwarded")
	}
	_ = wrappedOptional.Drain(ctx)
	if !optional.drained {
		t.Fatal("Drain was not forwarded")
	}
	_ = wrappedOptional.ForceStop(ctx)
	if !optional.forceStop {
		t.Fatal("ForceStop was not forwarded")
	}
}

func TestDependencyComponent_AdditiveDependencyContractRetainsIntrinsicDependencies(t *testing.T) {
	// Component has intrinsic dependencies: ["alpha", "beta"]
	inner := &mockResourceComp{
		name: "subsystem",
		deps: []string{"alpha", "beta"},
	}

	// Composition root declares: ["gamma", "alpha", "  ", "delta"]
	// Notice "beta" is NOT mentioned in composition-level dependencies.
	wrapped := dependencyComponent{
		Component:    inner,
		dependencies: []string{"gamma", "alpha", "  ", "delta"},
	}

	deps := wrapped.Dependencies()
	// Expected deterministic result:
	// 1. Composition dependencies first in order, trimmed and deduplicated: "gamma", "alpha", "delta"
	// 2. Unlisted intrinsic dependencies appended in order: "beta" ("alpha" was already seen)
	want := []string{"gamma", "alpha", "delta", "beta"}
	if len(deps) != len(want) {
		t.Fatalf("Dependencies() length = %d, want %d: got %+v", len(deps), len(want), deps)
	}
	for i, w := range want {
		if deps[i] != w {
			t.Fatalf("Dependencies()[%d] = %q, want %q (full: %+v)", i, deps[i], w, deps)
		}
	}
}
