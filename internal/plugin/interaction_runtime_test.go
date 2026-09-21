package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction"
)

type interactionRuntimeTestPlugin struct{}

func (interactionRuntimeTestPlugin) Name() string             { return "interaction-runtime-test" }
func (interactionRuntimeTestPlugin) Init() error              { return nil }
func (interactionRuntimeTestPlugin) Commands() []core.Command { return nil }
func (interactionRuntimeTestPlugin) FeatureSpec() feature.Spec {
	return feature.Spec{
		ID:   "interaction-runtime-test",
		Name: "Interaction Runtime Test",
		Interactions: []feature.Interaction{
			{
				ID:       "next",
				Kind:     feature.InteractionAction,
				Surfaces: execution.SurfaceAssistant,
				Policy:   feature.OwnerPolicy(execution.SurfaceAssistant),
			},
		},
	}
}

func TestManagerInteractionRuntimeFollowsPluginLifecycle(t *testing.T) {
	manager := NewManager(core.NewRouter("."))
	plugin := interactionRuntimeTestPlugin{}
	if err := manager.RegisterWithContext(context.Background(), plugin); err != nil {
		t.Fatalf("RegisterWithContext() error = %v", err)
	}

	runtime := manager.InteractionRuntime()
	if runtime == nil {
		t.Fatal("InteractionRuntime() returned nil")
	}
	first, err := runtime.Create(context.Background(), interaction.CreateRequest{
		FeatureID: plugin.Name(),
		Binding:   interaction.Binding{ActorID: 7},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	firstGeneration := first.Session.Scope.Generation
	data, err := runtime.CallbackData(context.Background(), first.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData() error = %v", err)
	}
	if _, err := runtime.ResolveCallback(context.Background(), data, interaction.Binding{ActorID: 7}); err != nil {
		t.Fatalf("ResolveCallback() error = %v", err)
	}

	if err := manager.Disable(context.Background(), plugin.Name()); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if !errors.Is(context.Cause(first.Context), interaction.ErrScopeStale) {
		t.Fatalf("first session cause = %v, want %v", context.Cause(first.Context), interaction.ErrScopeStale)
	}
	if stats := runtime.Stats(); stats.Sessions != 0 {
		t.Fatalf("sessions after disable = %d, want 0", stats.Sessions)
	}

	if err := manager.Enable(context.Background(), plugin.Name()); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	second, err := runtime.Create(context.Background(), interaction.CreateRequest{
		FeatureID: plugin.Name(),
		Binding:   interaction.Binding{ActorID: 7},
	})
	if err != nil {
		t.Fatalf("Create() after enable error = %v", err)
	}
	if second.Session.Scope.Generation == firstGeneration {
		t.Fatalf("generation after enable = %d, want a new generation", second.Session.Scope.Generation)
	}

	if err := manager.ShutdownWithContext(context.Background()); err != nil {
		t.Fatalf("ShutdownWithContext() error = %v", err)
	}
	if !errors.Is(context.Cause(second.Context), interaction.ErrScopeStale) {
		t.Fatalf("second session cause = %v, want %v", context.Cause(second.Context), interaction.ErrScopeStale)
	}
	if _, err := runtime.Create(context.Background(), interaction.CreateRequest{
		FeatureID: plugin.Name(),
		Binding:   interaction.Binding{ActorID: 9},
	}); !errors.Is(err, interaction.ErrClosed) {
		t.Fatalf("Create() after manager shutdown error = %v, want %v", err, interaction.ErrClosed)
	}
}
