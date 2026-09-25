package app

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/plugins/calculator"
	helpplugin "github.com/inipew/goultroid/plugins/help"
)

func TestP0BAssistantAbsenceIsSupportedComposition(t *testing.T) {
	var application App
	if renderer := application.SelfInlineRenderer(); renderer != nil {
		t.Fatalf("SelfInlineRenderer()=%T, want nil without userbot/Assistant composition", renderer)
	}

	provider := &p0SelfInlineServiceProvider{}
	if renderer := newSelfInlineRenderer(provider, nil); renderer != nil {
		t.Fatalf("newSelfInlineRenderer()=%T, want nil without Assistant identity", renderer)
	}
}

func TestP0BAssistantAbsenceDoesNotRemoveUserbotSurfaces(t *testing.T) {
	router := core.NewRouter(".")
	manager := plugin.NewManager(router)
	manager.SetInlineRegistry(inlineservice.NewRegistry())

	calculatorFeature := calculator.New()
	helpFeature := helpplugin.New(router)
	if err := manager.RegisterWithContext(context.Background(), calculatorFeature); err != nil {
		t.Fatalf("register calculator error=%v", err)
	}
	if err := manager.RegisterWithContext(context.Background(), helpFeature); err != nil {
		t.Fatalf("register help error=%v", err)
	}
	t.Cleanup(func() { _ = manager.ShutdownWithContext(context.Background()) })

	// Assistant is deliberately absent. Wiring must remain a no-op rather than
	// turning optional presentation into a requirement for userbot registration.
	wireSelfInlineRenderers(manager, &p0SelfInlineServiceProvider{}, nil, plugin.NewCapabilityGate())

	for _, name := range []string{"calc", "help"} {
		command, ok := router.Find(name)
		if !ok {
			t.Fatalf("userbot command %q disappeared when Assistant was absent", name)
		}
		if !command.IsAvailableOn(execution.SourceUserbot) {
			t.Fatalf("%s surfaces=%v, want SurfaceUserbot to remain available", name, command.Surfaces)
		}
	}
}
