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

	actions := manager.ActionDispatcher()
	oldCalls := 0
	oldRegistration, err := actions.Register(first.Session.Scope, plugin.Name(), "next", func(context.Context, interaction.Action) error {
		oldCalls++
		return nil
	})
	if err != nil {
		t.Fatalf("Register(old action) error = %v", err)
	}
	defer oldRegistration.Close()
	preparedOld, err := actions.Prepare(context.Background(), data, interaction.Binding{ActorID: 7})
	if err != nil {
		t.Fatalf("Prepare(old action) error = %v", err)
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
	if err := preparedOld.Dispatch(context.Background()); err == nil {
		t.Fatal("prepared old-generation action executed after disable")
	}
	if oldCalls != 0 {
		t.Fatalf("old-generation handler calls after disable = %d, want 0", oldCalls)
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
	secondData, err := runtime.CallbackData(context.Background(), second.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData(second) error = %v", err)
	}
	newCalls := 0
	newRegistration, err := actions.Register(second.Session.Scope, plugin.Name(), "next", func(context.Context, interaction.Action) error {
		newCalls++
		return nil
	})
	if err != nil {
		t.Fatalf("Register(new action) error = %v", err)
	}
	defer newRegistration.Close()
	if err := actions.Dispatch(context.Background(), secondData, interaction.Binding{ActorID: 7}); err != nil {
		t.Fatalf("Dispatch(new generation) error = %v", err)
	}
	if newCalls != 1 {
		t.Fatalf("new-generation handler calls = %d, want 1", newCalls)
	}
	preparedShutdown, err := actions.Prepare(context.Background(), secondData, interaction.Binding{ActorID: 7})
	if err != nil {
		t.Fatalf("Prepare(before shutdown) error = %v", err)
	}

	if err := manager.ShutdownWithContext(context.Background()); err != nil {
		t.Fatalf("ShutdownWithContext() error = %v", err)
	}
	if !errors.Is(context.Cause(second.Context), interaction.ErrScopeStale) {
		t.Fatalf("second session cause = %v, want %v", context.Cause(second.Context), interaction.ErrScopeStale)
	}
	if err := preparedShutdown.Dispatch(context.Background()); err == nil {
		t.Fatal("prepared action executed after manager shutdown")
	}
	if newCalls != 1 {
		t.Fatalf("handler crossed shutdown boundary, calls=%d", newCalls)
	}
	if _, err := runtime.Create(context.Background(), interaction.CreateRequest{
		FeatureID: plugin.Name(),
		Binding:   interaction.Binding{ActorID: 9},
	}); !errors.Is(err, interaction.ErrClosed) {
		t.Fatalf("Create() after manager shutdown error = %v, want %v", err, interaction.ErrClosed)
	}
}
