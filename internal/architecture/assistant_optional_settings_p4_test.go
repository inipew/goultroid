package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1F2SettingsProductionHasNoLegacyCallbackSurface(t *testing.T) {
	root := repositoryRoot(t)
	dir := filepath.Join(root, "plugins", "settings")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"/internal/services/callback",
		"callback.",
		"StateStore",
		"ScopedCallbackStore",
		"EncodeCallbackData(",
		"ParseCallbackData(",
		"HandleCallback(",
		"CallbackOptions(",
		"v1:settings",
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, token := range forbidden {
			if strings.Contains(source, token) {
				t.Fatalf("%s retained legacy Settings callback surface %q", name, token)
			}
		}
	}
}

func TestP1F2SettingsRoutesInteractiveSurfacesToA2(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "settings", "settings.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (p *Plugin) handleSettingsCommand")
	if start < 0 {
		t.Fatal("settings command handler missing")
	}
	end := strings.Index(source[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("settings command handler terminator missing")
	}
	body := source[start : start+end]
	for _, required := range []string{
		"ResolveBool(",
		"if useButtons {",
		"ctx.IsAssistant()",
		"openAssistantSettings(",
		"openNativeSettings(",
		"renderScreen(",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("settings command missing %q", required)
		}
	}
	for _, forbidden := range []string{"ReplyMarkup(", "EncodeCallbackData(", "StateStore"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("settings command retained legacy fallback %q", forbidden)
		}
	}
}

func TestP4AssistantSettingsRemainsSeparateA2Enhancement(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "assistant", "shell", "settings.go"): {
			"SettingsHomeState(",
			"ActionSettings",
			"ActionHome",
			"ActionClose",
		},
		filepath.Join(root, "plugins", "settings", "assistant_interaction.go"): {
			"AssistantFeatureID",
			"BindAssistant",
			"assistantSettingsScreenDashboard",
			"assistantSettingsSlotID",
			"rt.Engine.Begin(",
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
				t.Fatalf("Assistant settings a2 invariant missing from %s: %q", path, invariant)
			}
		}
	}
}
