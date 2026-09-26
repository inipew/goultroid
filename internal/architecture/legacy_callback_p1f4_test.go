package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1F4LegacyCallbackSubsystemIsGoneFromProduction(t *testing.T) {
	root := repositoryRoot(t)
	legacyImport := modulePath + "/internal/services/callback"
	forbidden := []string{
		"callback.Router",
		"callback.Handler",
		"callback.HandlerWithOptions",
		"callback.CallbackContext",
		"SetCallbackRegistrar(",
		"SetCallbackRouter(",
		"getCallbackRouter(",
		"CoreCallbackDispatcher",
		"dispatchCoreCallback(",
		"callbackRouter",
		"callbackCleanups",
	}

	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, "\"") == legacyImport {
				t.Errorf("P1-F4 legacy callback package import remains in production: %s", rel)
			}
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(raw)
		for _, marker := range forbidden {
			if strings.Contains(source, marker) {
				t.Errorf("P1-F4 legacy callback marker %q remains in production: %s", marker, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	callbackDir := filepath.Join(root, "internal", "services", "callback")
	if entries, err := os.ReadDir(callbackDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
				t.Errorf("P1-F4 legacy callback package file remains: %s", entry.Name())
			}
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestP1F4UnknownCallbackPolicyLivesAtIngress(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "telegram", "dispatcher_callback.go"): {
			"func (d *Dispatcher) answerUnknownCallback(",
			"unknownCallbackExpiredText = \"⌛ Interaction expired. Please reopen it.\"",
			"if string(event.Data) == \"noop\"",
			"d.answerUnknownCallback(ctx, evt)",
		},
		filepath.Join(root, "internal", "assistant", "client", "updates.go"): {
			"func answerUnknownAssistantCallback(",
			"unknownAssistantCallbackExpiredText = \"⌛ Interaction expired. Please reopen it.\"",
			"if string(data) == \"noop\"",
			"answerUnknownAssistantCallback(ctx, deps, update.QueryID, update.Data)",
		},
	}
	for path, required := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, marker := range required {
			if !strings.Contains(source, marker) {
				t.Errorf("P1-F4 explicit unknown callback policy missing %q from %s", marker, path)
			}
		}
	}
}
