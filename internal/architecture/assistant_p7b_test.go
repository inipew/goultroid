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

func TestP7BGroupRoleResolverHasNoBackgroundWorkersOrTickers(t *testing.T) {
	root := repositoryRoot(t)
	dir := filepath.Join(root, "internal", "assistant", "groupauth")
	var paths []string
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.GoStmt:
				t.Errorf("P7-B resolver %s starts background goroutine at %s", path, fset.Position(n.Pos()))
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
					t.Errorf("P7-B resolver %s installs background timer %s at %s",
						path, sel.Sel.Name, fset.Position(n.Pos()))
				}
			}
			return true
		})
	}
}
