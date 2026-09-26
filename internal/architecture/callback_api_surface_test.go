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
			"Register": {}, "HasHandler": {}, "GetHandler": {}, "TaskScope": {},
			"DispatchPrepared": {}, "StateStore": {},
		},
	}
	forbiddenFuncs := map[string]struct{}{
		"NewActionData": {}, "NewStateStore": {}, "NewScopedStateWriter": {},
		"EncodeCallbackData": {}, "EncodeCallbackDataChecked": {}, "ParseCallbackData": {},
	}
	callbackFiles := []string{
		filepath.Join(root, "internal", "services", "callback", "router.go"),
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
