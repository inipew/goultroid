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

func TestP2CProductionPresentationDoesNotExposeRawErrors(t *testing.T) {
	root := repositoryRoot(t)
	for _, relRoot := range []string{"plugins", filepath.Join("internal", "assistant")} {
		base := filepath.Join(root, relRoot)
		err := filepath.Walk(base, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, raw, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !p2cPresentationMethod(sel.Sel.Name) {
					return true
				}
				for _, arg := range call.Args {
					if p2cContainsRawErrorPresentation(arg) {
						t.Errorf("%s: .%s directly exposes an internal error; route through Context.Fail, core.UserMessage, or an explicit bounded sanitizer", path, sel.Sel.Name)
						break
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
}

func TestP2CKnownRawErrorHotspotsStayClosed(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		"plugins/settings/settings.go": {
			"EscapeHTML(err.Error())",
			"settings.cli.history_failed\", ui.EscapeHTML(err.Error())",
			"settings.cli.export_failed\", ui.EscapeHTML(err.Error())",
		},
		"plugins/myxl/assistant_interaction.go": {
			"ctx.Answer(fmt.Sprintf(\"Gagal",
			"html.EscapeString(err.Error())",
			"assistantRearm(ctx, state, fmt.Sprintf(\"❌ Gagal menyimpan alias: %v\"",
		},
		"internal/assistant/savedresponseadmin/feature.go": {
			"rearm(ctx, s, err.Error())",
		},
	}
	for rel, forbidden := range checks {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, marker := range forbidden {
			if strings.Contains(source, marker) {
				t.Errorf("P2-C raw-error hotspot returned in %s: %q", rel, marker)
			}
		}
	}
}

func TestP2COwnerExecDiagnosticExceptionRemainsNarrowAndBounded(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "plugins", "system", "system.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	// .exec intentionally returns command/process diagnostics to the Owner. Keep
	// this exception exact: owner-only, userbot-only, bounded, and HTML escaped.
	for _, required := range []string{
		"Name: \"exec\"",
		"Permission: core.PermissionOwner",
		"Surfaces: execution.SurfaceUserbot",
		"2*1024*1024",
		"len(output) <= 3500",
		"escapeHTML(output)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("owner exec diagnostic exception lost safety marker %q", required)
		}
	}
	if got := strings.Count(source, "fmt.Sprintf(\"Error: %v\", err)"); got != 2 {
		t.Fatalf("owner exec diagnostic raw-error allowlist count=%d, want exactly 2", got)
	}
}

func p2cPresentationMethod(name string) bool {
	switch name {
	case "Error", "Status", "Reply", "Edit", "EditOrReply", "Progress", "Success", "Result", "Answer":
		return true
	default:
		return false
	}
}

func p2cContainsRawErrorPresentation(expr ast.Expr) bool {
	raw := false
	ast.Inspect(expr, func(node ast.Node) bool {
		if raw {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if p2cIsErrorStringCall(call) || p2cIsRawErrorSprintf(call) {
			raw = true
			return false
		}
		return true
	})
	return raw
}

func p2cIsErrorStringCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Error" || len(call.Args) != 0 {
		return false
	}
	switch x := sel.X.(type) {
	case *ast.Ident:
		return p2cErrorLikeName(x.Name)
	case *ast.SelectorExpr:
		return p2cErrorLikeName(x.Sel.Name)
	default:
		return false
	}
}

func p2cIsRawErrorSprintf(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Sprintf" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "fmt" {
		return false
	}
	for _, arg := range call.Args[1:] {
		found := false
		ast.Inspect(arg, func(node ast.Node) bool {
			if found {
				return false
			}
			ident, ok := node.(*ast.Ident)
			if ok && p2cErrorLikeName(ident.Name) {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

func p2cErrorLikeName(name string) bool {
	name = strings.ToLower(name)
	return name == "err" || strings.HasSuffix(name, "err") || strings.HasSuffix(name, "error")
}
