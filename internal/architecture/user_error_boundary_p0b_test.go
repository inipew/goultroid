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

func TestP0BUserFacingErrorsUseTypedSafeBoundary(t *testing.T) {
	root := repositoryRoot(t)
	migrated := []string{
		"plugins/media/media.go",
		"plugins/profile/profile.go",
		"plugins/notes/notes.go",
		"plugins/clone/clone.go",
		"plugins/system/system.go",
		"plugins/myxl/myxl.go",
	}
	for _, relative := range migrated {
		path := filepath.Join(root, filepath.FromSlash(relative))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), ".Fail(") {
			t.Fatalf("%s is not migrated to the typed user-facing failure boundary", relative)
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, raw, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !p0bPresentationMethod(selector.Sel.Name) {
				return true
			}
			for _, arg := range call.Args {
				if p0bContainsInternalError(arg) {
					t.Errorf("%s still passes an internal error directly to .%s; use Context.Fail with explicit safe text", relative, selector.Sel.Name)
					break
				}
			}
			return true
		})
	}

	corePath := filepath.Join(root, "internal", "core", "user_error.go")
	coreRaw, err := os.ReadFile(corePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"type UserFacingError struct",
		"SafeUserMessage() string",
		"UserErrorPresented() bool",
		"execution.DispositionHandled",
		"func (c *Context) Fail(",
	} {
		if !strings.Contains(string(coreRaw), required) {
			t.Fatalf("typed user-error boundary missing %q", required)
		}
	}

	assistantPath := filepath.Join(root, "internal", "assistant", "command", "router.go")
	assistantRaw, err := os.ReadFile(assistantPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(assistantRaw), "core.UserErrorWasPresented(err)") {
		t.Fatal("Assistant feedback path can duplicate an already-presented safe error")
	}
}

func p0bPresentationMethod(name string) bool {
	switch name {
	case "Error", "Reply", "EditOrReply", "Status", "Success", "Progress", "Result":
		return true
	default:
		return false
	}
}

func p0bContainsInternalError(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		if found {
			return false
		}
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		switch ident.Name {
		case "err", "tmpErr", "bErr", "qErr", "captureErr", "cause":
			found = true
			return false
		default:
			return true
		}
	})
	return found
}
