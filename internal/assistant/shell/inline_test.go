package shell

import (
	"context"
	"strings"
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

func TestInlineHelpUsesCanonicalA2PresentationForSelfInlineBridge(t *testing.T) {
	featurePlugin := NewFeature()
	featurePlugin.SetHelpCommandProvider(func() []core.Command {
		return []core.Command{
			{
				Name:        "ping",
				Aliases:     []string{"p"},
				Description: "Check latency",
				Category:    "System",
				Surfaces:    execution.SurfaceUserbot,
			},
			{
				Name:        "download",
				Description: "Download media",
				Category:    "Media",
				Surfaces:    execution.SurfaceUserbot,
			},
		}
	})

	var help inlineservice.InlineHandlerV2
	for _, binding := range featurePlugin.InlineBindings() {
		if binding.InteractionID == InteractionInlineHelp {
			help, _ = binding.Handler.(inlineservice.InlineHandlerV2)
			break
		}
	}
	if help == nil {
		t.Fatal("typed inline help binding not found")
	}

	root, err := help.HandleInlineV2(&inlineservice.InlineContext{Ctx: context.Background()})
	if err != nil {
		t.Fatalf("root inline help error=%v", err)
	}
	if len(root.Results) != 1 {
		t.Fatalf("root inline help results=%d, want 1", len(root.Results))
	}
	result := root.Results[0]
	if result.ID != "assistant_help" || len(result.ActionRows) == 0 || len(result.InteractionState) == 0 || result.InteractionTTL != InteractionTTL {
		t.Fatalf("root inline help is not canonical typed a2 presentation: %+v", result)
	}
	if DecodeState(result.InteractionState).Screen != ScreenHelp {
		t.Fatalf("root inline help state=%+v", DecodeState(result.InteractionState))
	}
	for _, row := range result.ActionRows {
		for _, button := range row {
			if button.ActionID == ActionHome || button.ActionID == ActionClose {
				t.Fatalf("inline help leaked unsupported Assistant message chrome action %q", button.ActionID)
			}
		}
	}

	detail, err := help.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:  context.Background(),
		Args: []string{"p"},
	})
	if err != nil {
		t.Fatalf("command inline help error=%v", err)
	}
	if len(detail.Results) != 1 || detail.Results[0].ID != "assistant_help" || !strings.Contains(detail.Results[0].Text, "/ping") {
		t.Fatalf("exact command did not resolve canonical detail: %+v", detail.Results)
	}
	if len(detail.Results[0].ActionRows) == 0 || len(detail.Results[0].InteractionState) == 0 {
		t.Fatalf("exact command detail lost typed navigation: %+v", detail.Results[0])
	}
}

func TestInlineHelpActionsAreDeclaredOnInlineSurface(t *testing.T) {
	spec := NewFeature().FeatureSpec()
	for _, actionID := range append(
		[]string{ActionHelp, ActionHelpPrev, ActionHelpNext, ActionHelpCmdPrev, ActionHelpCmdNext, ActionHelpBack},
		append(HelpModuleSlotActionIDs(), HelpCommandSlotActionIDs()...)...,
	) {
		found := false
		for _, interaction := range spec.Interactions {
			if interaction.Kind == feature.InteractionAction && interaction.ID == actionID {
				found = true
				if !interaction.Surfaces.Supports(execution.SourceInline) || interaction.Policy.Permission != core.PermissionOwner {
					t.Fatalf("help action %q inline policy=%+v surfaces=%v", actionID, interaction.Policy, interaction.Surfaces)
				}
				break
			}
		}
		if !found {
			t.Fatalf("help action %q missing from feature spec", actionID)
		}
	}
}
