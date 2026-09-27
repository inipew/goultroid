package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestM2TelegramCallersUseNarrowCapabilities(t *testing.T) {
	root := repositoryRoot(t)
	paths := []string{
		"internal/core/executor.go",
		"internal/telegram/dispatcher.go",
		"internal/telegram/dispatcher_accessors.go",
		"internal/telegram/dispatcher_callback.go",
		"internal/telegram/dispatcher_dispatch.go",
		"internal/services/inline/engine.go",
		"internal/presentation/telegram/bridge.go",
		"internal/assistant/client/servicer.go",
		"internal/assistant/client/updates.go",
		"internal/assistant/client/interaction_ingress.go",
		"internal/assistant/client/audience_broadcast.go",
		"internal/assistant/command/router.go",
		"internal/assistant/command/servicer.go",
		"internal/app/scheduled_action.go",
	}
	for _, rel := range paths {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if strings.Contains(string(raw), "core.TelegramServicer") {
			t.Fatalf("%s still depends on broad core.TelegramServicer", rel)
		}
	}
}

func TestM2ContextCompatibilityFieldIsNoLongerBroad(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "core", "context.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	broad := regexp.MustCompile(`(?m)^\s*Svc\s+TelegramServicer\b`)
	if broad.MatchString(source) {
		t.Fatal("core.Context.Svc still uses broad TelegramServicer")
	}
	narrow := regexp.MustCompile(`(?m)^\s*Svc\s+CommandTelegramServicer\b`)
	if !narrow.MatchString(source) {
		t.Fatal("core.Context compatibility field must be narrowed to CommandTelegramServicer")
	}
	capabilities := regexp.MustCompile(`(?m)^\s*Telegram\s+TelegramCapabilities\b`)
	if !capabilities.MatchString(source) {
		t.Fatal("core.Context is missing TelegramCapabilities")
	}
}

func TestM2AssistantRemovedUnsupportedTelegramMegaStub(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "assistant", "client", "servicer.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "unsupportedTelegramServicer") {
		t.Fatal("assistant client restored unsupportedTelegramServicer mega-stub")
	}
}
