package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1F1CLegacyCallbackConsumerTopologyIsFrozen(t *testing.T) {
	root := repositoryRoot(t)
	markers := map[string]map[string]struct{}{
		"callback.NewRouter(": {"internal/app/wiring_core.go": {}},
		"SetCallbackRegistrar(": {
			"internal/app/app.go":        {},
			"internal/plugin/manager.go": {},
		},
		"p.(callback.Handler)": {"internal/plugin/manager.go": {}},
		"SetCallbackRouter(": {
			"internal/app/app.go":                       {},
			"internal/assistant/app.go":                 {},
			"internal/assistant/client/client.go":       {},
			"internal/telegram/dispatcher.go":           {},
			"internal/telegram/dispatcher_accessors.go": {},
		},
		"getCallbackRouter()": {
			"internal/telegram/dispatcher_accessors.go": {},
			"internal/telegram/dispatcher_callback.go":  {},
		},
		"dispatchCoreCallback(": {
			"internal/assistant/client/callback_dispatch.go": {},
			"internal/assistant/client/updates.go":           {},
		},
	}
	seen := make(map[string]map[string]struct{}, len(markers))
	for marker := range markers {
		seen[marker] = make(map[string]struct{})
	}
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
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		source := string(raw)
		for marker, allowed := range markers {
			if !strings.Contains(source, marker) {
				continue
			}
			if _, ok := allowed[rel]; !ok {
				t.Errorf("new legacy callback consumer edge %q outside allowlist: %s", marker, rel)
				continue
			}
			seen[marker][rel] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for marker, allowed := range markers {
		for rel := range allowed {
			if _, ok := seen[marker][rel]; !ok {
				t.Errorf("P1-F1 consumer allowlist is stale for %q: %s", marker, rel)
			}
		}
	}
}
