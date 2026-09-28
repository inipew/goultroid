package feature

import (
	"testing"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestRegistryInteractionCatalogViewFollowsRegistration(t *testing.T) {
	registry := NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 7}
	registration, err := registry.Register(Owner{ID: "demo", Scope: scope}, Spec{
		ID:                "demo",
		Name:              "Demo",
		DurabilityVersion: "1",
		Interactions: []Interaction{
			{
				ID:       "next",
				Kind:     InteractionAction,
				Surfaces: execution.SurfaceAssistant,
				Policy:   OwnerPolicy(execution.SurfaceAssistant),
			},
			{
				ID:       "home",
				Kind:     InteractionScreen,
				Surfaces: execution.SurfaceAssistant,
				Policy:   OwnerPolicy(execution.SurfaceAssistant),
			},
		},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	gotScope, ok := registry.FeatureScope("demo")
	if !ok || gotScope != scope {
		t.Fatalf("FeatureScope() = %+v, %v, want %+v, true", gotScope, ok, scope)
	}
	if !registry.HasAction("demo", "next") {
		t.Fatal("HasAction(next) = false, want true")
	}
	if got := registry.DurabilityVersion("demo"); got != "1" {
		t.Fatalf("durability version = %q", got)
	}
	if registry.HasAction("demo", "home") {
		t.Fatal("HasAction(home) = true for non-action interaction")
	}

	registration.Close()
	if got := registry.DurabilityVersion("demo"); got != "" {
		t.Fatalf("closed durability version = %q", got)
	}
	if _, ok := registry.FeatureScope("demo"); ok {
		t.Fatal("FeatureScope() remains visible after registration close")
	}
	if registry.HasAction("demo", "next") {
		t.Fatal("HasAction(next) remains true after registration close")
	}
}
