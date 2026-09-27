package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// M2 keeps Context.Svc only as a source-compatible fallback for legacy/test
// construction. Production Context literals must bind TelegramCapabilities so
// the compatibility aggregate cannot become a dependency again.
func TestM2ProductionContextLiteralsDoNotBindSvc(t *testing.T) {
	root := repositoryRoot(t)
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok || !isCoreContextComposite(literal.Type) {
				return true
			}
			for _, element := range literal.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				ident, ok := field.Key.(*ast.Ident)
				if ok && ident.Name == "Svc" {
					position := fset.Position(field.Pos())
					t.Errorf("production core.Context literal binds deprecated Svc at %s", position)
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

func isCoreContextComposite(expr ast.Expr) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil || selector.Sel.Name != "Context" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "core"
}

// Core facades must resolve their consumer-sized capability through the M2
// adapter. Direct reads of Context.Svc outside that adapter would silently
// restore the aggregate dependency that M2 removed.
func TestM2CoreOnlyCompatibilityAdapterReadsContextSvc(t *testing.T) {
	root := repositoryRoot(t)
	coreDir := filepath.Join(root, "internal", "core")
	fset := token.NewFileSet()

	entries, err := filepath.Glob(filepath.Join(coreDir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "context_telegram.go" {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel == nil || selector.Sel.Name != "Svc" {
				return true
			}
			position := fset.Position(selector.Pos())
			t.Errorf("core code bypasses TelegramCapabilities via Context.Svc at %s", position)
			return true
		})
	}
}
