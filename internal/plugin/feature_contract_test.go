package plugin

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
)

type featureContractTestPlugin struct{}

func (featureContractTestPlugin) Name() string { return "feature-contract-test" }
func (featureContractTestPlugin) Init() error  { return nil }
func (featureContractTestPlugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "surfacecmd",
			Description: "surface command",
			Permission:  core.PermissionOwner,
			Surfaces:    execution.SurfaceBotAndUser,
			Handler:     func(*core.Context) error { return nil },
		},
	}
}
func (featureContractTestPlugin) FeatureSpec() feature.Spec {
	return feature.Spec{
		ID:   "feature-contract-test",
		Name: "Feature Contract Test",
		Interactions: []feature.Interaction{
			{
				ID:       "home",
				Kind:     feature.InteractionScreen,
				Surfaces: execution.SurfaceAssistant,
				Policy:   feature.OwnerPolicy(execution.SurfaceAssistant),
			},
		},
	}
}

func TestManagerFeatureContractFollowsPluginGeneration(t *testing.T) {
	manager := NewManager(core.NewRouter("."))
	plugin := featureContractTestPlugin{}
	if err := manager.RegisterWithContext(context.Background(), plugin); err != nil {
		t.Fatalf("RegisterWithContext() error = %v", err)
	}

	catalog := manager.FeatureCatalog()
	if catalog == nil {
		t.Fatal("FeatureCatalog() returned nil")
	}
	entry, ok := catalog.Get(plugin.Name())
	if !ok {
		t.Fatal("feature contract not registered")
	}
	firstGeneration := entry.Owner.Scope.Generation
	if firstGeneration == 0 {
		t.Fatal("feature contract has zero lifecycle generation")
	}
	if command, ok := catalog.FindCommand(plugin.Name(), "surfacecmd"); !ok || !command.Surfaces.Supports(execution.SourceAssistant) {
		t.Fatalf("canonical assistant command surface missing: %+v, ok=%v", command, ok)
	}
	if _, ok := catalog.FindInteraction(plugin.Name(), feature.InteractionScreen, "home"); !ok {
		t.Fatal("declared interaction surface missing")
	}

	if err := manager.Disable(context.Background(), plugin.Name()); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if _, ok := catalog.Get(plugin.Name()); ok {
		t.Fatal("feature contract remains visible after disable")
	}

	if err := manager.Enable(context.Background(), plugin.Name()); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	entry, ok = catalog.Get(plugin.Name())
	if !ok {
		t.Fatal("feature contract not restored after enable")
	}
	if entry.Owner.Scope.Generation == firstGeneration {
		t.Fatalf("feature generation = %d, want new generation after re-enable", entry.Owner.Scope.Generation)
	}
}
