package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/runtime"
)

// TestU5_SupervisorImplementsForcedStopper verifies that Supervisor implements
// runtime.ForcedStopper and runtime.Quiescer as required by Phase U5.
func TestU5_SupervisorImplementsForcedStopper(t *testing.T) {
	var sup any = (*runtime.Supervisor)(nil)
	if _, ok := sup.(runtime.ForcedStopper); !ok {
		t.Fatal("Supervisor does not implement runtime.ForcedStopper")
	}
	if _, ok := sup.(runtime.Quiescer); !ok {
		t.Fatal("Supervisor does not implement runtime.Quiescer")
	}
	if _, ok := sup.(runtime.Component); !ok {
		t.Fatal("Supervisor does not implement runtime.Component")
	}
}

// TestU5_VoiceHasNoDetachedGoroutines ensures that internal/voice contains no
// detached goroutines (`go func` statements), preserving lifecycle context
// propagation and synchronous persistence invariants.
func TestU5_VoiceHasNoDetachedGoroutines(t *testing.T) {
	root := repositoryRoot(t)
	voiceDir := filepath.Join(root, "internal", "voice")
	fset := token.NewFileSet()

	var goStatements []string
	err := filepath.WalkDir(voiceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != voiceDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}

		ast.Inspect(file, func(n ast.Node) bool {
			if goStmt, ok := n.(*ast.GoStmt); ok {
				pos := fset.Position(goStmt.Pos())
				goStatements = append(goStatements, pos.String())
			}
			return true
		})
		return nil
	})

	if err != nil {
		t.Fatalf("walk voice directory: %v", err)
	}

	if len(goStatements) > 0 {
		t.Fatalf("internal/voice contains %d detached go statement(s): %v", len(goStatements), goStatements)
	}
}

// TestU5_VoiceQueueAndPlayerHaveNoBackgroundContextFallbacks asserts that
// internal/voice/queue.go and internal/voice/player.go do not instantiate
// fallback context.Background(), guaranteeing caller-context propagation.
func TestU5_VoiceQueueAndPlayerHaveNoBackgroundContextFallbacks(t *testing.T) {
	root := repositoryRoot(t)
	fset := token.NewFileSet()

	for _, filename := range []string{"queue.go", "player.go"} {
		filePath := filepath.Join(root, "internal", "voice", filename)
		file, err := parser.ParseFile(fset, filePath, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			xIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if xIdent.Name == "context" && sel.Sel.Name == "Background" {
				pos := fset.Position(call.Pos())
				t.Fatalf("forbidden context.Background() fallback found in %s at %s", filename, pos)
			}
			return true
		})
	}
}
