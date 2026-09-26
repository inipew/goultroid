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

func TestP1F3LegacyStateAndV1ProtocolAreGone(t *testing.T) {
	root := repositoryRoot(t)
	legacyImport := modulePath + "/internal/services/callback"
	retiredSelectors := map[string]struct{}{
		"StateStore":                {},
		"StateWriter":               {},
		"StateReader":               {},
		"StateScope":                {},
		"NewStateStore":             {},
		"NewScopedStateWriter":      {},
		"EncodeCallbackData":        {},
		"EncodeCallbackDataChecked": {},
		"ParseCallbackData":         {},
		"CallbackVersion1":          {},
		"ErrStateExpired":           {},
		"ErrStateNotFound":          {},
		"ErrStateConsumed":          {},
		"ErrStateScopeStale":        {},
		"HandlerWithStatePolicy":    {},
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
			callbackAlias = "callback"
			if spec.Name != nil {
				callbackAlias = spec.Name.Name
			}
		}
		if callbackAlias != "" && callbackAlias != "." && callbackAlias != "_" {
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != callbackAlias {
					return true
				}
				if _, retired := retiredSelectors[sel.Sel.Name]; retired {
					t.Errorf("P1-F3 retired legacy callback API %s.%s remains in production: %s", callbackAlias, sel.Sel.Name, rel)
				}
				return true
			})
		}

		if rel == "internal/module/module.go" {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(raw), "ScopedCallbackStore(") || strings.Contains(string(raw), "CallbackStore") {
				t.Errorf("P1-F3 retired module callback-state capability remains in production: %s", rel)
			}
		}

		if strings.HasPrefix(rel, "internal/services/callback/") {
			ast.Inspect(file, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if ok {
					if _, retired := retiredSelectors[ident.Name]; retired {
						t.Errorf("P1-F3 retired legacy callback state/protocol identifier %s remains in callback shell: %s", ident.Name, rel)
					}
				}
				return true
			})
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err == nil && strings.HasPrefix(value, "v1:") {
				t.Errorf("P1-F3 retired raw v1 callback payload remains in production: %s", rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP1F3LegacyCallbackPackageIsStateFreeShell(t *testing.T) {
	root := repositoryRoot(t)
	dir := filepath.Join(root, "internal", "services", "callback")
	expected := map[string]struct{}{"middleware.go": {}, "router.go": {}, "types.go": {}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if _, ok := expected[entry.Name()]; !ok {
			t.Errorf("unexpected production file remains in P1-F3 callback shell: %s", entry.Name())
			continue
		}
		seen[entry.Name()] = struct{}{}
	}
	for name := range expected {
		if _, ok := seen[name]; !ok {
			t.Errorf("P1-F3 callback shell inventory missing %s", name)
		}
	}
}
