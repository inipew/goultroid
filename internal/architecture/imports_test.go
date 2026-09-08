package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/inipew/goultroid"

func TestDependencyBoundaries(t *testing.T) {
	root := repositoryRoot(t)
	imports := collectImports(t, root)

	for pkg, deps := range imports {
		if strings.HasPrefix(pkg, modulePath+"/internal/database") || strings.HasPrefix(pkg, modulePath+"/internal/core") {
			for dep := range deps {
				if strings.HasPrefix(dep, modulePath+"/plugins/") {
					t.Errorf("%s must not depend on feature package %s", pkg, dep)
				}
			}
		}
		if strings.HasPrefix(pkg, modulePath+"/plugins/") {
			for dep := range deps {
				if dep == modulePath+"/internal/app" || strings.HasPrefix(dep, modulePath+"/internal/app/") {
					t.Errorf("feature package %s must not depend on app %s", pkg, dep)
				}
				if strings.HasPrefix(dep, modulePath+"/plugins/") && dep != pkg && !strings.HasPrefix(dep, pkg+"/") && !strings.HasPrefix(pkg, dep+"/") {
					t.Errorf("feature package %s must not depend on another feature %s", pkg, dep)
				}
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func collectImports(t *testing.T, root string) map[string]map[string]struct{} {
	t.Helper()
	result := make(map[string]map[string]struct{})
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
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
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		pkg := modulePath
		if rel != "." {
			pkg += "/" + filepath.ToSlash(rel)
		}
		set := result[pkg]
		if set == nil {
			set = make(map[string]struct{})
			result[pkg] = set
		}
		for _, spec := range f.Imports {
			pathValue := strings.Trim(spec.Path.Value, `"`)
			set[pathValue] = struct{}{}
		}
	}
	return result
}
