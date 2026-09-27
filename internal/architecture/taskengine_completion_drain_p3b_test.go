package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func TestP3BCompletionDeliveryDrainIsEventDriven(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "taskengine", "delivery.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, raw, 0)
	if err != nil {
		t.Fatal(err)
	}

	var drain *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "drain" || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		ident, ok := star.X.(*ast.Ident)
		if ok && ident.Name == "completionDelivery" {
			drain = fn
			break
		}
	}
	if drain == nil {
		t.Fatal("completionDelivery.drain not found")
	}

	forbidden := map[string]struct{}{
		"NewTicker": {},
		"Sleep":     {},
		"After":     {},
	}
	ast.Inspect(drain.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "time" {
			if _, blocked := forbidden[sel.Sel.Name]; blocked {
				t.Fatalf("completionDelivery.drain reintroduced polling via time.%s", sel.Sel.Name)
			}
		}
		return true
	})

	source := string(raw)
	for _, required := range []string{
		"drainCh chan struct{}",
		"func (d *completionDelivery) markBusy()",
		"func (d *completionDelivery) signalDrained()",
		"func (d *completionDelivery) drainSignal() (<-chan struct{}, bool)",
		"case <-wait:",
		"case <-ctx.Done():",
	} {
		if !containsP3B(source, required) {
			t.Fatalf("completion delivery event-driven drain missing %q", required)
		}
	}
}

func containsP3B(source, marker string) bool {
	return len(marker) == 0 || len(source) >= len(marker) && indexP3B(source, marker) >= 0
}

func indexP3B(source, marker string) int {
	for i := 0; i+len(marker) <= len(source); i++ {
		if source[i:i+len(marker)] == marker {
			return i
		}
	}
	return -1
}
