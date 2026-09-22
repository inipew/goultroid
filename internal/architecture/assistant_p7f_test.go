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

func TestP7FGroupStateHasNoGlobalSettingsOrBackgroundLifecycle(t *testing.T) {
	root := repositoryRoot(t)
	dir := filepath.Join(root, "internal", "services", "groupstate")
	fset := token.NewFileSet()

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", path, err)
			}
			if importPath == modulePath+"/internal/settings" ||
				strings.HasPrefix(importPath, modulePath+"/internal/settings/") {
				t.Errorf("P7-F groupstate must not depend on global/effective settings: %s imports %s", path, importPath)
			}
		}

		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.GoStmt:
				t.Errorf("P7-F groupstate %s starts background goroutine at %s", path, fset.Position(n.Pos()))
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "time" {
					return true
				}
				switch sel.Sel.Name {
				case "NewTicker", "Tick", "AfterFunc":
					t.Errorf("P7-F groupstate %s installs timer %s at %s",
						path, sel.Sel.Name, fset.Position(n.Pos()))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP7FGroupStateSchemaKeepsExplicitChatRevisionAndExpiry(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "services", "groupstate", "migration.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"assistant_group_state",
		"chat_id INTEGER NOT NULL",
		"namespace TEXT NOT NULL",
		"key TEXT NOT NULL",
		"revision INTEGER NOT NULL",
		"expires_at DATETIME",
		"PRIMARY KEY (chat_id, namespace, key)",
		"idx_assistant_group_state_expiry",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("P7-F schema missing invariant %q", required)
		}
	}
	for _, forbidden := range []string{
		"scope_type",
		"scope_id",
		"GetEffectiveSetting",
		"ScopeGlobal",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("P7-F schema leaked generic/global settings concept %q", forbidden)
		}
	}
}
