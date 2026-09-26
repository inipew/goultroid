package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1ACanonicalUIVocabularyFlowsFromPresentationToLegacyUI(t *testing.T) {
	root := repositoryRoot(t)

	vocabularyPath := filepath.Join(root, "internal", "presentation", "vocabulary.go")
	vocabularyRaw, err := os.ReadFile(vocabularyPath)
	if err != nil {
		t.Fatal(err)
	}
	vocabulary := string(vocabularyRaw)
	for _, required := range []string{
		"type ButtonRole uint8",
		"func ButtonLabel(role ButtonRole) string",
		"func ActionButton(",
		"func RoleActionButton(",
		"func URLButton(",
		"func SwitchInlineButton(",
	} {
		if !strings.Contains(vocabulary, required) {
			t.Fatalf("canonical presentation vocabulary missing %q", required)
		}
	}

	alertsPath := filepath.Join(root, "internal", "ui", "alerts.go")
	alertsRaw, err := os.ReadFile(alertsPath)
	if err != nil {
		t.Fatal(err)
	}
	alerts := string(alertsRaw)
	for _, required := range []string{
		"presentation.Success(msg).Render()",
		"presentation.Warning(msg).Render()",
		"presentation.Error(msg).Render()",
		"presentation.Progress(msg).Render()",
		"presentation.Information(msg).Render()",
	} {
		if !strings.Contains(alerts, required) {
			t.Fatalf("legacy alert adapter missing %q", required)
		}
	}

	buttonPath := filepath.Join(root, "internal", "ui", "button.go")
	buttonRaw, err := os.ReadFile(buttonPath)
	if err != nil {
		t.Fatal(err)
	}
	buttons := string(buttonRaw)
	if strings.Contains(buttons, `"✖ Close"`) || strings.Contains(buttons, `"◀ Back"`) {
		t.Fatal("legacy button helpers reintroduced non-canonical close/back labels")
	}
	for _, required := range []string{
		"NewRoleCallbackButton",
		"presentation.ButtonRoleClose",
		"presentation.ButtonRoleBack",
		"presentation.ButtonRoleConfirm",
		"presentation.ButtonRoleCancel",
	} {
		if !strings.Contains(buttons, required) {
			t.Fatalf("legacy button adapter missing %q", required)
		}
	}

	presentationDir := filepath.Join(root, "internal", "presentation")
	entries, err := os.ReadDir(presentationDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(presentationDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"github.com/inipew/goultroid/internal/ui"`) {
			t.Fatalf("presentation root must not depend back on legacy UI: %s", entry.Name())
		}
	}
}
