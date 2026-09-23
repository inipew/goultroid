package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8DWikipediaInlineUsesOwnedLifecycleAndBoundedLookup(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "plugins", "wikipedia", "wikipedia.go"): {
			"FeatureSpec() feature.Spec",
			"feature.InteractionInline",
			"feature.PublicPolicy(execution.SurfaceInline)",
			"InlineBindings() []inlineservice.Binding",
			"maxInlineResults          = 5",
			"CachePolicy() inlineservice.CachePolicy { return inlineservice.CacheGlobal }",
			"h.lookup.searchPages(ctx.Ctx, query, maxInlineResults)",
			"p.searchPages(ctx, q, 1)",
			"Wikipedia HTTP capability is unavailable",
		},
		filepath.Join(root, "internal", "plugin", "features.go"): {
			"inlineRegistry.RegisterOwned(",
			"inlineRegistration.Close()",
		},
		filepath.Join(root, "internal", "services", "inline", "engine.go"): {
			"context.WithTimeout(ctx, timeout)",
			"HandlerVersion(handler)",
		},
	}
	for path, required := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, invariant := range required {
			if !strings.Contains(source, invariant) {
				t.Errorf("P8-D invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP8DWikipediaOwnsNoSearchRuntimeOrCapabilityBypass(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "wikipedia", "wikipedia.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []string{
		"network.NewService(",
		"go func(",
		"time.NewTicker(",
		"time.Tick(",
		"time.AfterFunc(",
		"sync.Map",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("P8-D Wikipedia introduced forbidden runtime/capability bypass %q", forbidden)
		}
	}

	modulePath := filepath.Join(root, "plugins", "wikipedia", "module.go")
	moduleRaw, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	moduleSource := string(moduleRaw)
	if !strings.Contains(moduleSource, "Capabilities: []string{plugin.CapHTTP}") {
		t.Fatal("P8-D Wikipedia module must remain HTTP-capability scoped")
	}
	for _, forbidden := range []string{"plugin.CapTelegramRaw", "plugin.CapProcessExecute"} {
		if strings.Contains(moduleSource, forbidden) {
			t.Errorf("P8-D Wikipedia module gained unrelated privileged capability %q", forbidden)
		}
	}
}
