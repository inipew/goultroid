package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1F1DLegacyCallbackOwnershipIsFrozen(t *testing.T) {
	root := repositoryRoot(t)
	callbackDir := filepath.Join(root, "internal", "services", "callback")

	expectedProductionFiles := map[string]struct{}{
		"lifecycle.go":     {},
		"middleware.go":    {},
		"router.go":        {},
		"scope.go":         {},
		"scoped_writer.go": {},
		"state.go":         {},
		"store.go":         {},
		"types.go":         {},
	}
	seen := make(map[string]struct{}, len(expectedProductionFiles))
	fset := token.NewFileSet()

	entries, err := os.ReadDir(callbackDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if _, ok := expectedProductionFiles[entry.Name()]; !ok {
			t.Errorf("new production file in legacy callback subsystem outside P1-F1-D inventory: %s", entry.Name())
			continue
		}
		seen[entry.Name()] = struct{}{}

		path := filepath.Join(callbackDir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if _, ok := n.(*ast.GoStmt); ok {
				t.Errorf("legacy callback subsystem must not acquire owned background goroutines: %s", entry.Name())
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "time" {
				switch sel.Sel.Name {
				case "NewTicker", "Tick", "AfterFunc":
					t.Errorf("legacy callback subsystem must remain free of periodic/detached timer workers before reclamation: %s", entry.Name())
				}
			}
			if ident.Name == "ratelimit" && sel.Sel.Name == "New" {
				t.Errorf("legacy callback subsystem must not own a private rate limiter: %s", entry.Name())
			}
			return true
		})
	}

	for name := range expectedProductionFiles {
		if _, ok := seen[name]; !ok {
			t.Errorf("P1-F1-D callback file inventory is stale; remove or reclassify %s", name)
		}
	}
}
