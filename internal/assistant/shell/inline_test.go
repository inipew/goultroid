package shell

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestInlineBindingsUseFeatureCatalog(t *testing.T) {
	catalog := feature.NewRegistry()
	registration, err := catalog.Register(
		feature.Owner{
			ID:    "demo",
			Scope: tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1},
		},
		feature.Spec{
			ID:   "demo",
			Name: "Demo Feature",
			Commands: []feature.CommandSurface{{
				Name:        "hello",
				Description: "Say hello from demo",
				Usage:       "hello <name>",
				Category:    "Demo",
				Surfaces:    execution.SurfaceAssistant,
				Policy:      feature.PublicPolicy(execution.SurfaceAssistant),
			}},
		},
	)
	if err != nil {
		t.Fatalf("catalog register error = %v", err)
	}
	defer registration.Close()

	featurePlugin := NewFeature()
	featurePlugin.SetInlineCatalog(catalog)
	featurePlugin.SetStartTime(time.Now().Add(-time.Minute))

	bindings := featurePlugin.InlineBindings()
	if len(bindings) != 3 {
		t.Fatalf("inline bindings=%d, want 3", len(bindings))
	}

	var help inlineservice.InlineHandler
	for _, binding := range bindings {
		if binding.InteractionID == InteractionInlineHelp {
			help = binding.Handler
			break
		}
	}
	if help == nil {
		t.Fatal("inline help binding not found")
	}
	results, err := help.HandleInline(&inlineservice.InlineContext{
		Ctx:  context.Background(),
		Args: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("help HandleInline() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("help results=%d, want 1", len(results))
	}
	if results[0].Title != "hello" || results[0].Description != "Say hello from demo" {
		t.Fatalf("help result not derived from FeatureCatalog: %+v", results[0])
	}
}

func TestInlineFeatureSpecPolicies(t *testing.T) {
	spec, err := feature.BindCanonicalCommands(NewFeature().FeatureSpec(), nil)
	if err != nil {
		t.Fatalf("BindCanonicalCommands() error = %v", err)
	}

	interactions := map[string]feature.Interaction{}
	for _, interaction := range spec.Interactions {
		if interaction.Kind == feature.InteractionInline {
			interactions[interaction.ID] = interaction
		}
	}
	if len(interactions) != 3 {
		t.Fatalf("inline interaction count=%d, want 3", len(interactions))
	}
	if interactions[InteractionInlineHelp].Policy.Permission != core.PermissionOwner {
		t.Fatalf("inline help permission=%v, want owner", interactions[InteractionInlineHelp].Policy.Permission)
	}
	if interactions[InteractionInlineRoot].Policy.Permission != core.PermissionEveryone ||
		interactions[InteractionInlinePing].Policy.Permission != core.PermissionEveryone {
		t.Fatal("root/ping inline surfaces should remain public")
	}
}
