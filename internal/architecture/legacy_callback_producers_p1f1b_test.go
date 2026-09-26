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

func TestP1F1BLegacyCallbackProducersRemainConfined(t *testing.T) {
	root := repositoryRoot(t)
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
		if strings.HasPrefix(rel, "internal/services/callback/") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				switch fun := node.Fun.(type) {
				case *ast.Ident:
					if fun.Name == "EncodeCallbackData" || fun.Name == "EncodeCallbackDataChecked" {
						t.Errorf("production legacy callback encoder call outside callback subsystem: %s", rel)
					}
				case *ast.SelectorExpr:
					if fun.Sel != nil && (fun.Sel.Name == "EncodeCallbackData" || fun.Sel.Name == "EncodeCallbackDataChecked") {
						t.Errorf("production legacy callback encoder call outside callback subsystem: %s", rel)
					}
				}
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(node.Value)
				if err != nil {
					return true
				}
				if strings.HasPrefix(value, "v1:") {
					t.Errorf("raw production legacy v1 callback payload outside callback subsystem: %s", rel)
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
