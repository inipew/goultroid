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

// TestU1_CompositionRootHasSingleAuthorities verifies that the composition root
// (internal/app) constructs exactly one Runtime, one TaskEngine, one JobManager,
// one Scheduler Engine, and one Telegram Client/Executor, preserving strict single-authority
// boundaries without duplicate or shadow execution/lifecycle coordinators.
func TestU1_CompositionRootHasSingleAuthorities(t *testing.T) {
	root := repositoryRoot(t)
	appPath := filepath.Join(root, "internal", "app")
	fset := token.NewFileSet()

	runtimeNewCalls := 0
	taskEngineNewCalls := 0
	jobsNewCalls := 0
	schedNewCalls := 0
	telegramClientNewCalls := 0

	err := filepath.WalkDir(appPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != appPath {
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
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}

			switch pkgIdent.Name {
			case "runtime":
				if sel.Sel.Name == "New" {
					runtimeNewCalls++
				}
			case "taskengine":
				if sel.Sel.Name == "NewEngine" {
					taskEngineNewCalls++
				}
			case "jobs":
				if sel.Sel.Name == "NewManagerWithPorts" || sel.Sel.Name == "NewManager" {
					jobsNewCalls++
				}
			case "scheduler":
				if sel.Sel.Name == "NewEngine" {
					schedNewCalls++
				}
			case "telegram":
				if sel.Sel.Name == "NewClient" {
					telegramClientNewCalls++
				}
			}
			return true
		})
		return nil
	})

	if err != nil {
		t.Fatalf("walk app directory: %v", err)
	}

	if runtimeNewCalls != 1 {
		t.Fatalf("composition root runtime.New calls = %d, want exactly 1", runtimeNewCalls)
	}
	if taskEngineNewCalls != 1 {
		t.Fatalf("composition root taskengine.NewEngine calls = %d, want exactly 1", taskEngineNewCalls)
	}
	if jobsNewCalls != 1 {
		t.Fatalf("composition root jobs.NewManager* calls = %d, want exactly 1", jobsNewCalls)
	}
	if schedNewCalls != 1 {
		t.Fatalf("composition root scheduler.NewEngine calls = %d, want exactly 1", schedNewCalls)
	}
	if telegramClientNewCalls != 1 {
		t.Fatalf("composition root telegram.NewClient calls = %d, want exactly 1", telegramClientNewCalls)
	}
}

// TestU1_NoForbiddenGlobalManagers verifies that no forbidden global managers,
// System Coordinators, or parallel execution pools exist anywhere in production code.
func TestU1_NoForbiddenGlobalManagers(t *testing.T) {
	root := repositoryRoot(t)
	forbiddenIdentNames := []string{
		"SystemCoordinator",
		"GlobalManager",
		"GlobalRegistry",
		"GlobalExecutor",
		"GlobalWorkerPool",
	}

	fset := token.NewFileSet()
	for _, sub := range []string{"internal", "cmd", "plugins"} {
		subDir := filepath.Join(root, sub)
		err := filepath.WalkDir(subDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "vendor" || d.Name() == ".git" {
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

			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					for _, forbidden := range forbiddenIdentNames {
						if ts.Name.Name == forbidden {
							t.Fatalf("forbidden global architecture type %q defined at %s", forbidden, path)
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sub, err)
		}
	}
}
