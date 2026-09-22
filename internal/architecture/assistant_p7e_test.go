package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestP7EReadOnlyCanaryCannotCallGroupMutations(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "info", "info.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	forbidden := map[string]struct{}{
		"Pin":                         {},
		"Unpin":                       {},
		"Ban":                         {},
		"Unban":                       {},
		"Kick":                        {},
		"Mute":                        {},
		"Unmute":                      {},
		"Purge":                       {},
		"Promote":                     {},
		"Demote":                      {},
		"EditChatDefaultBannedRights": {},
	}

	var handler *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "handleChatInfo" {
			handler = fn
			break
		}
	}
	if handler == nil {
		t.Fatal("P7-E read-only canary handleChatInfo is missing")
	}

	ast.Inspect(handler.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, blocked := forbidden[selector.Sel.Name]; blocked {
			t.Errorf("P7-E read-only canary calls mutation %s at %s",
				selector.Sel.Name, fset.Position(call.Pos()))
		}
		return true
	})
}

func TestP7EManagerFoundationKeepsP7GMutationFence(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "command", "servicer.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	required := map[string]bool{
		"PinMessage":                  false,
		"UnpinMessage":                false,
		"BanUser":                     false,
		"UnbanUser":                   false,
		"KickUser":                    false,
		"MuteUser":                    false,
		"UnmuteUser":                  false,
		"PurgeMessages":               false,
		"PromoteAdmin":                false,
		"DemoteAdmin":                 false,
		"EditChatDefaultBannedRights": false,
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil {
			continue
		}
		if _, tracked := required[fn.Name.Name]; !tracked {
			continue
		}

		fenced := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if ident, ok := node.(*ast.Ident); ok && ident.Name == "ErrGroupMutationUnavailable" {
				fenced = true
			}
			return true
		})
		if !fenced {
			t.Errorf("P7-E closure unexpectedly opened mutation method %s", fn.Name.Name)
		}
		required[fn.Name.Name] = true
	}

	for name, found := range required {
		if !found {
			t.Errorf("P7-E expected mutation fence method %s is missing", name)
		}
	}
}
