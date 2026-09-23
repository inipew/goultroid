package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8FLocaleUsesCanonicalSettingsAndA2Surface(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "settings", "defaults.go"): {
			"Key:           "locale"",
			"Type:          TypeEnum",
			"AllowedValues: []string{"en", "id"}",
		},
		filepath.Join(root, "internal", "assistant", "shell", "shell.go"): {
			"InteractionLanguage",
			"ActionLanguageEnglish",
			"ActionLanguageIndonesian",
			"SetSettingsService",
		},
		filepath.Join(root, "internal", "assistant", "shell", "locale.go"): {
			"LocaleSettingNamespace = "ui"",
			"LocaleSettingKey       = "locale"",
			"svc.ResolveString(",
			"LanguageView(",
		},
		filepath.Join(root, "internal", "assistant", "client", "shell_locale.go"): {
			"ctx.UpdateState(state, 0)",
			"svc.SetRegisteredResult(",
			"settings.ScopeUser",
		},
		filepath.Join(root, "internal", "app", "app.go"): {
			"assistantShell.SetSettingsService(domServices.settingsService)",
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
				t.Errorf("P8-F invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP8FLocalePresentationCoversAssistantShellAndInline(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "assistant", "shell", "shell.go"): {
			"Locale    string",
			"assistant.button.language",
			"assistant.home.header",
			"assistant.status.header",
		},
		filepath.Join(root, "internal", "assistant", "shell", "help.go"): {
			"Locale   string",
			"assistant.help.title",
			"assistant.help.module_header",
		},
		filepath.Join(root, "internal", "assistant", "shell", "settings.go"): {
			"Locale   string",
			"LocalizedSettingDefinition",
			"assistant.settings.header",
		},
		filepath.Join(root, "internal", "assistant", "shell", "inline.go"): {
			"inlineContextLocale",
			"ResolveLocale(ctx.Ctx, svc, ctx.UserID, 0)",
			"assistant.inline.title",
			"assistant.inline.feature",
		},
		filepath.Join(root, "internal", "assistant", "client", "interaction_help.go"): {
			"shell.PublicStartView(c.Username(), c.shellLocale(",
			"Locale: c.shellInteractionLocale(ctx)",
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
				t.Errorf("P8-F locale presentation invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP8FLocaleAddsNoSecondLocaleRuntime(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("internal", "assistant", "shell", "locale.go"),
		filepath.Join("internal", "assistant", "client", "shell_locale.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{
			"go func(",
			"time.NewTicker(",
			"time.Tick(",
			"time.AfterFunc(",
			"sync.Map",
			"map[int64]",
			"map[string]",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("P8-F locale introduced forbidden runtime/state %q in %s", forbidden, rel)
			}
		}
	}
}
