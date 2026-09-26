package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1DNativeSettingsUsesA2WithoutAssistantOrLegacyCallbackDependency(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "settings", "native_interaction.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Contains(source, "/internal/assistant") {
		t.Fatal("native Settings must not depend on Assistant transport")
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

func TestP1DTextOnlyGatePrecedesBothA2SessionBegins(t *testing.T) {
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
	buttonGate := strings.Index(body, "if useButtons {")
	assistantBegin := strings.Index(body, "openAssistantSettings(")
	nativeBegin := strings.Index(body, "openNativeSettings(")
	textRender := strings.Index(body, "renderScreen(")
	if resolve < 0 || buttonGate < 0 || assistantBegin < 0 || nativeBegin < 0 || textRender < 0 {
		t.Fatal("settings command a2/text-only gates incomplete")
	}
	if !(resolve < buttonGate && buttonGate < assistantBegin && assistantBegin < nativeBegin && nativeBegin < textRender) {
		t.Fatal("settings must resolve inline_buttons before any a2 Begin and retain text-only render fallback")
	}
}
