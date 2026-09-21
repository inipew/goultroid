package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestCanonicalCallbackAPISurface(t *testing.T) {
	root := repositoryRoot(t)
	fset := token.NewFileSet()

	forbiddenMethods := map[string]map[string]struct{}{
		"Router": {
			"Register":         {},
			"HasHandler":       {},
			"GetHandler":       {},
			"TaskScope":        {},
			"DispatchPrepared": {},
			"StateStore":       {},
		},
		"StateStore": {
			"Get":        {},
			"GetEntry":   {},
			"ClaimEntry": {},
			"Consume":    {},
			"Delete":     {},
		},
	}
	forbiddenFuncs := map[string]struct{}{
		"NewActionData": {},
	}

	callbackFiles := []string{
		filepath.Join(root, "internal", "services", "callback", "router.go"),
		filepath.Join(root, "internal", "services", "callback", "store.go"),
		filepath.Join(root, "internal", "services", "callback", "types.go"),
	}
	for _, path := range callbackFiles {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Recv == nil {
				if _, forbidden := forbiddenFuncs[fn.Name.Name]; forbidden {
					t.Errorf("callback package re-exposes retired function %s in %s", fn.Name.Name, path)
				}
				continue
			}
			if len(fn.Recv.List) != 1 {
				continue
			}
			recv := receiverTypeName(fn.Recv.List[0].Type)
			if methods := forbiddenMethods[recv]; methods != nil {
				if _, forbidden := methods[fn.Name.Name]; forbidden {
					t.Errorf("callback %s re-exposes retired method %s in %s", recv, fn.Name.Name, path)
				}
			}
		}
	}

	modulePath := filepath.Join(root, "internal", "module", "module.go")
	moduleFile, err := parser.ParseFile(fset, modulePath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", modulePath, err)
	}
	ast.Inspect(moduleFile, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != "TelegramRuntime" {
			return true
		}
		st, ok := typeSpec.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.Name == "Callbacks" {
					t.Error("module.TelegramRuntime must not expose callback.Router")
				}
			}
		}
		return false
	})
}

func receiverTypeName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		if id, ok := v.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}
