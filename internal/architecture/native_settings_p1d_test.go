package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1DNativeSettingsUsesA2WithoutLegacyCallbackDependency(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "settings", "native_interaction.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Contains(source, "/internal/assistant") {
		t.Fatal("native Settings must not depend on Assistant")
	}
	if strings.Contains(source, "/internal/services/callback") || strings.Contains(source, "EncodeCallbackData(") {
		t.Fatal("native Settings must not generate legacy callback payloads")
	}
	for _, required := range []string{
		"func (p *Plugin) NativeFeatureID() string",
		"func (p *Plugin) BindNative(",
		"rt.Interactions.RegisterAction(",
		"rt.Interactions.Begin(",
		"ctx.Transition(",
		"applySettingMutation(",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("native Settings missing %q", required)
		}
	}
}

func TestP1DTextOnlyGatePrecedesNativeSessionBegin(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "settings", "settings.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (p *Plugin) handleSettingsCommand")
	if start < 0 {
		t.Fatal("settings command handler not found")
	}
	end := strings.Index(source[start:], "\nfunc ")
	if end < 0 {
		end = len(source) - start
	}
	body := source[start : start+end]
	resolve := strings.Index(body, "ResolveBool(")
	begin := strings.Index(body, "openNativeSettings(")
	textRender := strings.Index(body, "renderScreenMode(")
	if resolve < 0 || begin < 0 || textRender < 0 || resolve > begin || begin > textRender {
		t.Fatal("settings command must resolve inline_buttons before native Begin and retain text-only render fallback")
	}
	if !strings.Contains(body, "if useButtons && !ctx.IsAssistant()") {
		t.Fatal("native Settings a2 gate is missing")
	}
}
