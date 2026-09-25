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

func TestCommandUISendsStayBehindContextFacade(t *testing.T) {
	root := repositoryRoot(t)
	pluginsDir := filepath.Join(root, "plugins")
	fset := token.NewFileSet()

	err := filepath.Walk(pluginsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !rawCommandUIServiceMethod(sel.Sel.Name) {
					return true
				}
				svcSel, ok := sel.X.(*ast.SelectorExpr)
				if !ok || svcSel.Sel.Name != "Svc" {
					return true
				}
				ctxID, ok := svcSel.X.(*ast.Ident)
				if !ok || ctxID.Name != "ctx" {
					return true
				}
				rel, _ := filepath.Rel(root, path)
				if rel == "plugins/userlog/userlog.go" && fn.Name.Name == "handleSetLog" && sel.Sel.Name == "SendMessage" {
					return true
				}
				t.Errorf("%s:%d %s uses ctx.Svc.%s for command UI; use Context semantic/message facade", rel, fset.Position(call.Pos()).Line, fn.Name.Name, sel.Sel.Name)
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func rawCommandUIServiceMethod(name string) bool {
	switch name {
	case "SendMessage", "SendMessageWithMarkup", "EditMessage", "EditMessageMarkup", "EditMessageMarkupOnly":
		return true
	default:
		return false
	}
}

func TestCommandSemanticResponsesUseSemanticFacade(t *testing.T) {
	root := repositoryRoot(t)
	pluginsDir := filepath.Join(root, "plugins")
	fset := token.NewFileSet()

	err := filepath.Walk(pluginsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "EditOrReply" {
				return true
			}
			ctxID, ok := sel.X.(*ast.Ident)
			if !ok || ctxID.Name != "ctx" {
				return true
			}
			text, ok := semanticLiteral(call.Args[0])
			if !ok || !hasManualSemanticPrefix(text) {
				return true
			}
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s:%d uses manual semantic prefix through EditOrReply; use Status/Success/Error/Progress/Result", rel, fset.Position(call.Pos()).Line)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func semanticLiteral(expr ast.Expr) (string, bool) {
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		value, err := strconv.Unquote(lit.Value)
		return value, err == nil
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Sprintf" {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "fmt" {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func hasManualSemanticPrefix(text string) bool {
	text = strings.TrimSpace(text)
	for _, prefix := range []string{"❌", "✅", "ℹ️", "⚠️", "⏳"} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
