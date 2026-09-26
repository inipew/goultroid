package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestP1F1ELegacyCallbackProductionSurfaceIsExactlyFrozen(t *testing.T) {
	root := repositoryRoot(t)
	legacyImport := modulePath + "/internal/services/callback"
	allowed := map[string]map[string]struct{}{
		"internal/app/dependencies.go":              {"Router": {}},
		"internal/app/wiring_core.go":               {"NewRouter": {}},
		"internal/assistant/client/servicer.go":     {"PreparedCallback": {}},
		"internal/plugin/manager.go":                {"Handler": {}, "Registration": {}},
		"internal/telegram/dispatcher.go":           {"Router": {}},
		"internal/telegram/dispatcher_accessors.go": {"Router": {}},
		"internal/ui/toast.go": {
			"CallbackContext":        {},
			"ErrHandlerNotFound":     {},
			"ErrInvalidCallbackData": {},
		},
	}
	seen := make(map[string]map[string]struct{}, len(allowed))
	for rel := range allowed {
		seen[rel] = make(map[string]struct{})
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
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		callbackAlias := ""
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, "\"") != legacyImport {
				continue
			}
			if _, ok := allowed[rel]; !ok {
				t.Errorf("new production legacy callback importer outside P1-F1 closure: %s", rel)
				continue
			}
			callbackAlias = "callback"
			if spec.Name != nil {
				callbackAlias = spec.Name.Name
			}
			if callbackAlias == "." || callbackAlias == "_" {
				t.Errorf("legacy callback import must remain explicit: %s", rel)
				callbackAlias = ""
			}
		}
		if callbackAlias != "" {
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != callbackAlias {
					return true
				}
				if _, ok := allowed[rel][sel.Sel.Name]; !ok {
					t.Errorf("new legacy callback API use %s.%s outside allowlist: %s", callbackAlias, sel.Sel.Name, rel)
					return true
				}
				seen[rel][sel.Sel.Name] = struct{}{}
				return true
			})
		}
		if strings.HasPrefix(rel, "internal/services/callback/") {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err == nil && strings.HasPrefix(value, "v1:") {
				t.Errorf("raw production legacy v1 callback payload outside callback subsystem: %s", rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, symbols := range allowed {
		for symbol := range symbols {
			if _, ok := seen[rel][symbol]; !ok {
				t.Errorf("P1-F1 callback symbol allowlist is stale; remove or reclassify %s.%s", rel, symbol)
			}
		}
	}
}

func TestP1F1ESettingsAndMyXLRemainZeroLegacy(t *testing.T) {
	root := repositoryRoot(t)
	for _, plugin := range []string{"settings", "myxl"} {
		dir := filepath.Join(root, "plugins", plugin)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			source := string(raw)
			for _, forbidden := range []string{
				"/internal/services/callback", "callback.", "StateStore", "ScopedCallbackStore",
				"SetStateStore", "EncodeCallbackData(", "ParseCallbackData(", "RequiresCallbackState",
				"CallbackOptions(", "HandleCallback(", "v1:" + plugin,
			} {
				if strings.Contains(source, forbidden) {
					t.Errorf("%s/%s reintroduced legacy callback surface %q", plugin, entry.Name(), forbidden)
				}
			}
		}
	}
}
