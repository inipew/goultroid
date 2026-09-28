package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestM4JobsKeepsSingleManagerAndResponsibilityOwnership(t *testing.T) {
	root := repositoryRoot(t)
	jobsDir := filepath.Join(root, "internal", "jobs")
	fset := token.NewFileSet()

	managerDefs := 0
	managerPath := ""
	wantMethods := map[string]string{
		"ProcessDueSchedules":      "manager_schedule.go",
		"ensureRetryWorkersLocked": "manager_retry.go",
		"retryLoop":                "manager_retry.go",
		"watchAttempt":             "manager_retry.go",
		"driveAttempt":             "manager_attempt.go",
		"commitAttemptResult":      "manager_attempt.go",
		"durableCoordinatorLoop":   "manager_recovery.go",
		"Recover":                  "manager_recovery.go",
	}
	foundMethods := make(map[string]string, len(wantMethods))

	err := filepath.WalkDir(jobsDir, func(path string, entry fs.DirEntry, walkErr error) error {
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
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil || ts.Name.Name != "Manager" {
						continue
					}
					if _, ok := ts.Type.(*ast.StructType); ok {
						managerDefs++
						managerPath = filepath.Base(path)
					}
				}
			case *ast.FuncDecl:
				if d.Name == nil {
					continue
				}
				if _, tracked := wantMethods[d.Name.Name]; tracked {
					foundMethods[d.Name.Name] = filepath.Base(path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if managerDefs != 1 || managerPath != "manager.go" {
		t.Fatalf("jobs Manager ownership = defs:%d file:%s, want one Manager in manager.go", managerDefs, managerPath)
	}
	for method, wantFile := range wantMethods {
		if got := foundMethods[method]; got != wantFile {
			t.Fatalf("jobs responsibility %s owned by %q, want %q", method, got, wantFile)
		}
	}
}

func TestM4JobsManagerSplitKeepsExistingGoroutineAuthorities(t *testing.T) {
	root := repositoryRoot(t)
	jobsDir := filepath.Join(root, "internal", "jobs")
	fset := token.NewFileSet()
	type goSite struct {
		file string
		fn   string
	}
	var sites []goSite

	err := filepath.WalkDir(jobsDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "manager") || !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if _, ok := node.(*ast.GoStmt); ok {
					sites = append(sites, goSite{file: base, fn: fn.Name.Name})
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 2 {
		t.Fatalf("manager*.go goroutine sites = %+v, want exactly two existing authorities", sites)
	}
	seen := map[goSite]bool{}
	for _, site := range sites {
		seen[site] = true
	}
	for _, want := range []goSite{
		{file: "manager.go", fn: "Start"},
		{file: "manager_retry.go", fn: "ensureRetryWorkersLocked"},
	} {
		if !seen[want] {
			t.Fatalf("missing existing jobs goroutine authority %+v; got %+v", want, sites)
		}
	}
}

func TestM4JobsMutableCoordinatorStateStaysOnManager(t *testing.T) {
	root := repositoryRoot(t)
	jobsDir := filepath.Join(root, "internal", "jobs")
	fset := token.NewFileSet()
	owned := map[string]struct{}{
		"retryQueue": {}, "recoveryWake": {}, "outboxWake": {}, "tracked": {},
		"retryRemaining": {}, "retryQueued": {}, "retryActive": {}, "workersRemaining": {},
	}
	managerFields := make(map[string]bool, len(owned))

	err := filepath.WalkDir(jobsDir, func(path string, entry fs.DirEntry, walkErr error) error {
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
						if _, tracked := owned[name.Name]; !tracked {
							continue
						}
						if ts.Name.Name != "Manager" {
							t.Fatalf("jobs mutable field %s gained second owner %s in %s", name.Name, ts.Name.Name, filepath.Base(path))
						}
						managerFields[name.Name] = true
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name := range owned {
		if !managerFields[name] {
			t.Fatalf("jobs Manager lost mutable coordinator field %s", name)
		}
	}
}
