package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1CNativeInteractionAdapterIsAssistantIndependent(t *testing.T) {
	root := repositoryRoot(t)
	adapterPath := filepath.Join(root, "internal", "interaction", "native", "adapter.go")
	raw, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Contains(source, "/internal/assistant") {
		t.Fatal("native interaction adapter must not depend on Assistant")
	}
	for _, required := range []string{
		"orchestration.New(sessions, actions, port)",
		"prepared.Dispatch(taskCtx)",
		"rootinteraction.OwnsCallbackData(event.Data)",
		"feature.AdmitInteraction(",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("native interaction adapter missing %q", required)
		}
	}
}

func TestP1CNativeCallbackPrecedesLegacyRouter(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "telegram", "dispatcher_callback.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, functionName := range []string{"func (d *Dispatcher) OnBotCallbackQuery", "func (d *Dispatcher) OnInlineBotCallbackQuery"} {
		start := strings.Index(source, functionName)
		if start < 0 {
			t.Fatalf("missing %s", functionName)
		}
		next := strings.Index(source[start+len(functionName):], "\nfunc ")
		end := len(source)
		if next >= 0 {
			end = start + len(functionName) + next
		}
		body := source[start:end]
		nativeIndex := strings.Index(body, "dispatchNativeInteraction(ctx, evt)")
		legacyIndex := strings.Index(body, "d.getCallbackRouter()")
		if nativeIndex < 0 || legacyIndex < 0 || nativeIndex > legacyIndex {
			t.Fatalf("%s must route native a2 before legacy callback state", functionName)
		}
	}
}

func TestP1CNativeFoundationWiredWithoutAssistantGate(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "app", "app.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	nativeIndex := strings.Index(source, "nativeinteraction.New(")
	assistantIndex := strings.Index(source, "if tgRuntime.assistant != nil")
	if nativeIndex < 0 || assistantIndex < 0 || nativeIndex > assistantIndex {
		t.Fatal("native interaction foundation must be wired before optional Assistant composition")
	}
	for _, required := range []string{
		"tgRuntime.dispatcher.SetNativeInteractions(nativeInteractions)",
		"pluginManager.SetNativeInteractions(nativeInteractions)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("native interaction composition missing %q", required)
		}
	}
}
