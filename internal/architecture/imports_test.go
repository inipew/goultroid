package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/inipew/goultroid"

func TestDependencyBoundaries(t *testing.T) {
	root := repositoryRoot(t)
	imports := collectImports(t, root)

	for pkg, deps := range imports {
		// Tier 1 (internal/database, internal/core) must not import Tier 3 (plugins/*), Tier 2 (internal/services/*), or Tier 4 (internal/app/*)
		if strings.HasPrefix(pkg, modulePath+"/internal/database") || strings.HasPrefix(pkg, modulePath+"/internal/core") {
			for dep := range deps {
				if strings.HasPrefix(dep, modulePath+"/plugins/") {
					t.Errorf("%s must not depend on feature package %s", pkg, dep)
				}
				if strings.HasPrefix(dep, modulePath+"/internal/services/") {
					t.Errorf("%s must not depend on services package %s", pkg, dep)
				}
				if strings.HasPrefix(dep, modulePath+"/internal/app") {
					t.Errorf("%s must not depend on app package %s", pkg, dep)
				}
			}
		}

		// Tier 2 (internal/services/*) must not import Tier 3 (plugins/*) or Tier 4 (internal/app/*)
		if strings.HasPrefix(pkg, modulePath+"/internal/services/") {
			for dep := range deps {
				if strings.HasPrefix(dep, modulePath+"/plugins/") {
					t.Errorf("%s must not depend on feature package %s", pkg, dep)
				}
				if strings.HasPrefix(dep, modulePath+"/internal/app") {
					t.Errorf("%s must not depend on app package %s", pkg, dep)
				}
			}
		}

		// Tier 3 (plugins/*) must not import Tier 4 (internal/app/*), cmd/*, or sibling plugins
		if strings.HasPrefix(pkg, modulePath+"/plugins/") {
			for dep := range deps {
				if dep == modulePath+"/internal/app" || strings.HasPrefix(dep, modulePath+"/internal/app/") {
					t.Errorf("feature package %s must not depend on app %s", pkg, dep)
				}
				if strings.HasPrefix(dep, modulePath+"/cmd/") {
					t.Errorf("feature package %s must not depend on cmd package %s", pkg, dep)
				}
				if strings.HasPrefix(dep, modulePath+"/plugins/") && dep != pkg && !strings.HasPrefix(dep, pkg+"/") && !strings.HasPrefix(pkg, dep+"/") {
					t.Errorf("feature package %s must not depend on another feature %s", pkg, dep)
				}
			}
		}
	}
}

func TestDatabaseGenericBoundary(t *testing.T) {
	root := repositoryRoot(t)
	dbDir := filepath.Join(root, "internal", "database")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dbDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse database package: %v", err)
	}

	forbiddenKeywords := []string{
		"clone",
		"afk",
		"note",
		"filter",
		"blacklist",
		"sudo",
		"pmpermit",
		"voice",
		"moderation",
	}

	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				// 1. Check methods on DB receiver
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil && len(fn.Recv.List) > 0 {
					recvType := ""
					switch rt := fn.Recv.List[0].Type.(type) {
					case *ast.Ident:
						recvType = rt.Name
					case *ast.StarExpr:
						if id, ok := rt.X.(*ast.Ident); ok {
							recvType = id.Name
						}
					}
					if recvType == "DB" {
						methodLower := strings.ToLower(fn.Name.Name)
						for _, kw := range forbiddenKeywords {
							if strings.Contains(methodLower, kw) {
								t.Errorf("internal/database.DB contains forbidden domain method %s (must belong to feature-owned repository)", fn.Name.Name)
							}
						}
					}
				}

				// 2. Check type declarations in internal/database
				if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
					for _, spec := range gd.Specs {
						if ts, ok := spec.(*ast.TypeSpec); ok {
							typeLower := strings.ToLower(ts.Name.Name)
							for _, kw := range forbiddenKeywords {
								if strings.Contains(typeLower, kw) {
									t.Errorf("internal/database declares forbidden domain type %s (must belong to feature-owned repository)", ts.Name.Name)
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestLegacyFeatureDatabaseSurfaceIsExplicit(t *testing.T) {
	root := repositoryRoot(t)
	dbDir := filepath.Join(root, "internal", "database")
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		t.Fatal(err)
	}

	// These are known remaining legacy feature-owned persistence files. The list is
	// intentionally explicit so a new feature cannot silently add persistence here.
	allowedLegacy := map[string]struct{}{
		"addon.go":     {},
		"scheduler.go": {},
		"settings.go":  {},
		"userlog.go":   {},
	}

	var legacy []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if _, ok := allowedLegacy[entry.Name()]; ok {
			legacy = append(legacy, entry.Name())
		}
	}
	sort.Strings(legacy)

	want := make([]string, 0, len(allowedLegacy))
	for name := range allowedLegacy {
		want = append(want, name)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(legacy, want) {
		t.Fatalf("legacy feature persistence surface changed unexpectedly: got %v, want %v; migrate an existing feature before adding another file", legacy, want)
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
