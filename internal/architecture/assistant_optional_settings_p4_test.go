package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP4NativeSettingsDoesNotDependOnAssistantNavigation(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "settings", "settings.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []string{
		`callback.EncodeCallbackData("assistant"`,
		`"assistant", "start"`,
		`internal/assistant`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("native settings still depends on Assistant navigation: %q", forbidden)
		}
	}
	for _, required := range []string{
		`callback.EncodeCallbackData("settings", callback.ActionNav`,
		`callback.EncodeCallbackData("settings", callback.ActionClose`,
		`ui.NewCallbackButton("🏠 Home"`,
		`ui.NewCallbackButton("🔙 Back to "`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("native settings navigation invariant missing: %q", required)
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
		filepath.Join(root, "internal", "assistant", "client", "interaction_settings_test.go"): {
			"TestAssistantShellSettingsNavigationUsesCentralService",
			"SetSettingsService",
			"assistantshell.ActionSettings",
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
				t.Fatalf("Assistant settings enhancement invariant missing from %s: %q", path, invariant)
			}
		}
	}
}
