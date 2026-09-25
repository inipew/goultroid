package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1UserbotHelpPrefersCanonicalSelfInlineWithNativeFallback(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "plugins", "help", "help.go"): {
			"SetSelfInlineRenderer",
			"p.renderer.Render",
			"selfinline.FallbackSafe",
			"handleNativeHelp",
		},
		filepath.Join(root, "plugins", "help", "module.go"): {
			"plugin.CapTelegramRead",
			"plugin.CapTelegramSendMessage",
		},
		filepath.Join(root, "internal", "assistant", "shell", "inline.go"): {
			"ActionRows:       selection.View.Rows",
			"InteractionState: selection.State",
			"InteractionTTL:   InteractionTTL",
		},
		filepath.Join(root, "internal", "assistant", "shell", "shell.go"): {
			"helpPolicy := feature.OwnerPolicy(assistant | inlineSurface)",
		},
		filepath.Join(root, "internal", "app", "app.go"): {
			"SetHelpCommandProvider",
			"CommandsForSurface(execution.SourceUserbot)",
		},
		filepath.Join(root, "internal", "assistant", "client", "interaction_help.go"): {
			"shellHelpPresentation(ctx, view)",
		},
		filepath.Join(root, "internal", "assistant", "client", "shell_interaction.go"): {
			"shellHelpPresentation(ctx, view)",
			"admitShellContinuation",
		},
		filepath.Join(root, "internal", "assistant", "client", "shell_continuation.go"): {
			"AdmitInteractionIdentity(interaction, execution.SourceInline",
			"AdmitInteraction(interaction, execution.SourceAssistant",
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
				t.Fatalf("P1 help progressive-enhancement invariant missing from %s: %q", path, invariant)
			}
		}
	}

	helpSource, err := os.ReadFile(filepath.Join(root, "plugins", "help", "help.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"Interactive help is unavailable because the Assistant inline renderer is not running.",
		"Unable to open help through the Assistant:",
	} {
		if strings.Contains(string(helpSource), forbidden) {
			t.Fatalf("P1 help still exposes Assistant as a userbot requirement: %q", forbidden)
		}
	}
}

func TestP1UserbotHelpDoesNotCreateSecondInteractionRuntime(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("plugins", "help", "help.go"),
		filepath.Join("internal", "assistant", "shell", "inline_help.go"),
		filepath.Join("internal", "assistant", "client", "help_commands.go"),
		filepath.Join("internal", "assistant", "client", "shell_continuation.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{
			"interaction.NewRuntime(",
			"taskengine.New(",
			"NewRPCExecutor(",
			"callback.NewStateStore(",
			"go func(",
			"time.NewTicker(",
		} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("P1 help introduced forbidden duplicate runtime/state %q in %s", forbidden, rel)
			}
		}
	}
}
