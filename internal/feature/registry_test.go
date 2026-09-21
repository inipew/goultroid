package feature

import (
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestRegistryRegistrationLifecycle(t *testing.T) {
	registry := NewRegistry()
	spec := Spec{
		ID:   "demo",
		Name: "Demo",
		Interactions: []Interaction{
			{
				ID:       "home",
				Kind:     InteractionScreen,
				Surfaces: execution.SurfaceAssistant,
				Policy:   OwnerPolicy(execution.SurfaceAssistant),
			},
		},
	}
	owner := Owner{ID: "demo", Scope: tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1}}
	registration, err := registry.Register(owner, spec)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	entry, ok := registry.Get("demo")
	if !ok {
		t.Fatal("registered feature not found")
	}
	if entry.Owner.Scope != owner.Scope {
		t.Fatalf("owner scope = %+v, want %+v", entry.Owner.Scope, owner.Scope)
	}
	if got := registry.ForSurface(execution.SourceAssistant); len(got) != 1 {
		t.Fatalf("ForSurface(assistant) = %d entries, want 1", len(got))
	}
	registration.Close()
	if _, ok := registry.Get("demo"); ok {
		t.Fatal("feature remains registered after Close")
	}
}

func TestRegistryRejectsDuplicateFeature(t *testing.T) {
	registry := NewRegistry()
	spec := Spec{ID: "demo", Name: "Demo"}
	owner := Owner{ID: "demo", Scope: tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1}}
	first, err := registry.Register(owner, spec)
	if err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	defer first.Close()
	_, err = registry.Register(Owner{ID: "demo", Scope: tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 2}}, spec)
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("second Register() error = %v, want %v", err, ErrAlreadyRegistered)
	}
}

func TestRegistryReturnsDefensiveCopies(t *testing.T) {
	registry := NewRegistry()
	bound, err := BindCanonicalCommands(Spec{ID: "demo", Name: "Demo"}, nil)
	if err != nil {
		t.Fatalf("BindCanonicalCommands() error = %v", err)
	}
	registration, err := registry.Register(
		Owner{ID: "demo", Scope: tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1}},
		bound,
	)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	defer registration.Close()

	entry, ok := registry.Get("demo")
	if !ok {
		t.Fatal("registered feature not found")
	}
	entry.Spec.Name = "mutated"

	again, ok := registry.Get("demo")
	if !ok {
		t.Fatal("registered feature not found on second read")
	}
	if again.Spec.Name != "Demo" {
		t.Fatalf("stored name = %q, want Demo", again.Spec.Name)
	}
}

func TestRegistryStaleCleanupCannotRemoveNewGeneration(t *testing.T) {
	registry := NewRegistry()
	spec := Spec{ID: "demo", Name: "Demo"}

	first, err := registry.Register(
		Owner{ID: "demo", Scope: tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1}},
		spec,
	)
	if err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	first.Close()

	second, err := registry.Register(
		Owner{ID: "demo", Scope: tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 2}},
		spec,
	)
	if err != nil {
		t.Fatalf("second Register() error = %v", err)
	}
	defer second.Close()

	first.Close()
	entry, ok := registry.Get("demo")
	if !ok {
		t.Fatal("stale cleanup removed the new feature generation")
	}
	if entry.Owner.Scope.Generation != 2 {
		t.Fatalf("generation = %d, want 2", entry.Owner.Scope.Generation)
	}
}
