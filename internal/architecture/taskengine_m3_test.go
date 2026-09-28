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

func TestM3TaskEngineControlInboxHasSingleSendSite(t *testing.T) {
	root := repositoryRoot(t)
	taskengineDir := filepath.Join(root, "internal", "taskengine")
	fset := token.NewFileSet()
	type sendSite struct {
		file string
		fn   string
	}
	var sites []sendSite

	err := filepath.WalkDir(taskengineDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
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
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				send, ok := node.(*ast.SendStmt)
				if !ok {
					return true
				}
				isInbox := false
				switch channel := send.Chan.(type) {
				case *ast.Ident:
					isInbox = channel.Name == "inbox"
				case *ast.SelectorExpr:
					isInbox = channel.Sel != nil && channel.Sel.Name == "inbox"
				}
				if isInbox {
					sites = append(sites, sendSite{file: filepath.Base(path), fn: fn.Name.Name})
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].file != "engine.go" || sites[0].fn != "enqueueRequest" {
		t.Fatalf("TaskEngine control inbox send sites = %+v, want only engine.go:enqueueRequest", sites)
	}
}

func TestM3TaskEngineMutableExecutionStateStaysOnEngine(t *testing.T) {
	root := repositoryRoot(t)
	taskengineDir := filepath.Join(root, "internal", "taskengine")
	fset := token.NewFileSet()
	owned := map[string]struct{}{
		"registry": {}, "idleSlots": {}, "workerRunning": {}, "resourceUsed": {},
		"commitWaiters": {}, "cancelledScopes": {}, "terminalOrder": {},
	}

	err := filepath.WalkDir(taskengineDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
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
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range st.Fields.List {
					for _, name := range field.Names {
						if _, tracked := owned[name.Name]; tracked && ts.Name.Name != "Engine" {
							t.Fatalf("mutable TaskEngine field %s gained second owner %s in %s", name.Name, ts.Name.Name, filepath.Base(path))
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
