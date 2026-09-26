package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1E1MyXLNativeQuotaUsesA2WithoutAssistantOrLegacyCallback(t *testing.T) {
	root := repositoryRoot(t)
	nativePath := filepath.Join(root, "plugins", "myxl", "native_interaction.go")
	raw, err := os.ReadFile(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	native := string(raw)
	if strings.Contains(native, "/internal/assistant") || strings.Contains(native, "/internal/services/callback") {
		t.Fatal("native MyXL quota adapter must not depend on Assistant or legacy callback state")
	}
	for _, required := range []string{
		"func (p *Plugin) NativeFeatureID() string",
		"func (p *Plugin) BindNative(",
		"RegisterPreparedAction(",
		"ExecutionTimeout: nativeQuotaRefreshExec",
		"rt.Interactions.Begin(",
		"ctx.Transition(",
	} {
		if !strings.Contains(native, required) {
			t.Fatalf("native MyXL quota adapter missing %q", required)
		}
	}
}

func TestP1E1MyXLNoLongerIssuesLegacyRefreshCallbacks(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "myxl", "myxl.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Contains(source, `EncodeCallbackData("myxl", "refresh"`) || strings.Contains(source, "buildRefreshMarkup(") {
		t.Fatal("MyXL native quota path still issues legacy v1 refresh callbacks")
	}
}
