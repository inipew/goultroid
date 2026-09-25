package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP0AssistantInlineCapabilityPreflightUsesTelegramBotIdentity(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "assistant", "client", "identity.go"): {
			"GetBotInlinePlaceholder()",
			"ErrInlineDisabled",
			"InlineUsername()",
			"@BotFather /setinline",
		},
		filepath.Join(root, "internal", "assistant", "client", "client.go"): {
			"inlineCapability(user)",
			"@BotFather /setinline",
		},
		filepath.Join(root, "internal", "app", "selfinline.go"): {
			"NewWithIdentity(",
			"assistantclient.ErrInlineDisabled",
			"selfinline.ErrInlineDisabled",
		},
		filepath.Join(root, "internal", "presentation", "selfinline", "diagnostics.go"): {
			"tg.IsBotInlineDisabled(err)",
			"ErrInlineDisabled",
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
				t.Fatalf("inline capability preflight invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP0AssistantInlineCapabilityPreflightAddsNoBackgroundRuntime(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("internal", "assistant", "client", "identity.go"),
		filepath.Join("internal", "presentation", "selfinline", "render.go"),
		filepath.Join("internal", "presentation", "selfinline", "diagnostics.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{"go func(", "time.NewTicker(", "time.Tick(", "time.AfterFunc(", "sync.Map"} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("inline capability preflight introduced background/runtime state %q in %s", forbidden, rel)
			}
		}
	}
}
