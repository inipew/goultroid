package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestM3TaskEngineKeepsSingleEngineAndCoordinator(t *testing.T) {
	root := repositoryRoot(t)
	taskengineDir := filepath.Join(root, "internal", "taskengine")
	fset := token.NewFileSet()

	engineDefs := 0
	runLoops := 0
	var enginePath string
	var runLoopPath string

	err := filepath.WalkDir(taskengineDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "vendor" || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil || ts.Name.Name != "Engine" {
						continue
					}
					if _, ok := ts.Type.(*ast.StructType); ok {
						engineDefs++
						enginePath = path
					}
				}
			case *ast.FuncDecl:
				if d.Name == nil || d.Name.Name != "runLoop" || d.Recv == nil || len(d.Recv.List) != 1 {
					continue
				}
				ptr, ok := d.Recv.List[0].Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				ident, ok := ptr.X.(*ast.Ident)
				if ok && ident.Name == "Engine" {
					runLoops++
					runLoopPath = path
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if engineDefs != 1 {
		t.Fatalf("TaskEngine Engine struct definitions = %d, want exactly 1", engineDefs)
	}
	if filepath.Base(enginePath) != "engine_state.go" {
		t.Fatalf("TaskEngine Engine ownership moved to %s, want engine_state.go", enginePath)
	}
	if runLoops != 1 {
		t.Fatalf("TaskEngine runLoop coordinators = %d, want exactly 1", runLoops)
	}
	if filepath.Base(runLoopPath) != "engine.go" {
		t.Fatalf("TaskEngine runLoop moved to %s, want engine.go", runLoopPath)
	}
}

func TestM3TaskEngineResponsibilityFilesExist(t *testing.T) {
	root := repositoryRoot(t)
	for _, name := range []string{
		"engine_config.go",
		"engine_state.go",
		"engine_admission.go",
		"engine_dispatch.go",
		"engine_completion.go",
		"engine_api.go",
	} {
		path := filepath.Join(root, "internal", "taskengine", name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing M3 TaskEngine responsibility file %s: %v", name, err)
		}
	}
}
