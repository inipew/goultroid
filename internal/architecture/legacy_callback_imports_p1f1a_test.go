package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1F1ALegacyCallbackProductionImportsAreFrozen(t *testing.T) {
	root := repositoryRoot(t)
	legacyImport := modulePath + "/internal/services/callback"
	allowed := map[string]struct{}{
		"internal/app/app.go":                       {},
		"internal/app/dependencies.go":              {},
		"internal/app/wiring_core.go":               {},
		"internal/assistant/client/servicer.go":     {},
		"internal/module/module.go":                 {},
		"internal/plugin/manager.go":                {},
		"internal/telegram/dispatcher.go":           {},
		"internal/telegram/dispatcher_accessors.go": {},
		"internal/ui/toast.go":                      {},
	}
	seen := make(map[string]struct{}, len(allowed))
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

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, "\"") != legacyImport {
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if _, ok := allowed[rel]; !ok {
				t.Errorf("new production legacy callback import outside P1-F1-A freeze allowlist: %s", rel)
				continue
			}
			seen[rel] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for rel := range allowed {
		if _, ok := seen[rel]; !ok {
			t.Errorf("P1-F1-A legacy callback import allowlist is stale; remove or reclassify %s", rel)
		}
	}
}
