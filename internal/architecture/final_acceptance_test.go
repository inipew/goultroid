package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestFinalFeatureBoundaries(t *testing.T) {
	root := repositoryRoot(t)
	imports := collectImports(t, root)

	for pkg, deps := range imports {
		if strings.HasPrefix(pkg, modulePath+"/plugins/") {
			for dep := range deps {
				if strings.HasPrefix(dep, modulePath+"/internal/app") ||
					strings.HasPrefix(dep, modulePath+"/cmd/") {
					t.Errorf("feature %s imports composition package %s", pkg, dep)
				}
				if strings.HasPrefix(dep, modulePath+"/plugins/") &&
					dep != pkg && !strings.HasPrefix(dep, pkg+"/") && !strings.HasPrefix(pkg, dep+"/") {
					t.Errorf("feature %s imports another feature %s", pkg, dep)
				}
			}
		}

		if strings.HasPrefix(pkg, modulePath+"/internal/execution") {
			for dep := range deps {
				if strings.HasPrefix(dep, modulePath+"/plugins/") ||
					strings.HasPrefix(dep, modulePath+"/internal/app") {
					t.Errorf("execution package %s must remain independent of feature/composition packages: %s", pkg, dep)
				}
			}
		}
	}
}

func TestNoFeatureInitRegistration(t *testing.T) {
	root := repositoryRoot(t)
	pluginsDir := filepath.Join(root, "plugins")
	var files []string
	err := filepath.Walk(pluginsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)

	fset := token.NewFileSet()
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "init" {
				t.Errorf("feature file %s uses init() registration; registration must be explicit and deterministic", path)
			}
		}
	}
}
