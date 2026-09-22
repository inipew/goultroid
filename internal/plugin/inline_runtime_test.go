package plugin

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
)

type inlineRuntimeTestPlugin struct{}

func (inlineRuntimeTestPlugin) Name() string             { return "inline-runtime-test" }
func (inlineRuntimeTestPlugin) Init() error              { return nil }
func (inlineRuntimeTestPlugin) Commands() []core.Command { return nil }

func (inlineRuntimeTestPlugin) FeatureSpec() feature.Spec {
	return feature.Spec{
		ID:   "inline-runtime-test",
		Name: "Inline Runtime Test",
		Interactions: []feature.Interaction{{
			ID:          "lookup",
			Kind:        feature.InteractionInline,
			Description: "Lifecycle-scoped inline lookup",
			Surfaces:    execution.SurfaceInline,
			Policy:      feature.OwnerPolicy(execution.SurfaceInline),
		}},
	}
}

func (inlineRuntimeTestPlugin) InlineBindings() []inlineservice.Binding {
	return []inlineservice.Binding{{
		InteractionID: "lookup",
		Handler:       &inlineRuntimeTestHandler{},
	}}
}

type inlineRuntimeTestHandler struct{}

func (*inlineRuntimeTestHandler) Pattern() string { return "p4lookup" }
func (*inlineRuntimeTestHandler) Description() string {
	return "legacy description must not own policy"
}
func (*inlineRuntimeTestHandler) HandleInline(*inlineservice.InlineContext) ([]inlineservice.InlineResult, error) {
	return []inlineservice.InlineResult{{ID: "ok", Title: "ok", Text: "ok"}}, nil
}

func TestManagerInlineRuntimeFollowsPluginLifecycle(t *testing.T) {
	registry := inlineservice.NewRegistry()
	manager := NewManager(core.NewRouter("."))
	manager.SetInlineRegistry(registry)
	plugin := inlineRuntimeTestPlugin{}

	if err := manager.RegisterWithContext(context.Background(), plugin); err != nil {
		t.Fatalf("RegisterWithContext() error = %v", err)
	}
	first, ok := registry.ResolveOwned("p4lookup one")
	if !ok {
		t.Fatal("feature-owned inline handler was not registered")
	}
	if first.FeatureID != plugin.Name() || first.InteractionID != "lookup" {
		t.Fatalf("unexpected ownership: feature=%q interaction=%q", first.FeatureID, first.InteractionID)
	}
	if first.Scope.IsZero() {
		t.Fatal("feature-owned inline handler has zero lifecycle scope")
	}
	if len(first.Args) != 1 || first.Args[0] != "one" {
		t.Fatalf("resolved args = %v, want [one]", first.Args)
	}
	extended, ok := first.Handler.(inlineservice.InlineHandlerV2)
	if !ok {
		t.Fatalf("feature inline wrapper does not implement InlineHandlerV2: %T", first.Handler)
	}
	if access := extended.AccessPolicy(); !access.OwnerOnly || access.SudoOnly {
		t.Fatalf("FeatureSpec owner policy was not canonical: %+v", access)
	}
	firstVersion := inlineservice.HandlerVersion(first.Handler)
	firstGeneration := first.Scope.Generation

	if err := manager.Disable(context.Background(), plugin.Name()); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if _, ok := registry.ResolveOwned("p4lookup one"); ok {
		t.Fatal("disabled plugin left inline handler registered")
	}

	if err := manager.Enable(context.Background(), plugin.Name()); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	second, ok := registry.ResolveOwned("p4lookup two")
	if !ok {
		t.Fatal("re-enabled plugin did not restore inline handler")
	}
	if second.Scope.Generation == firstGeneration {
		t.Fatalf("re-enabled inline scope generation=%d, want new generation", second.Scope.Generation)
	}
	if secondVersion := inlineservice.HandlerVersion(second.Handler); secondVersion == "" || secondVersion == firstVersion {
		t.Fatalf("inline cache version did not move with generation: first=%q second=%q", firstVersion, secondVersion)
	}

	if err := manager.ShutdownWithContext(context.Background()); err != nil {
		t.Fatalf("ShutdownWithContext() error = %v", err)
	}
	if _, ok := registry.ResolveOwned("p4lookup"); ok {
		t.Fatal("shutdown left feature inline handler registered")
	}
}
