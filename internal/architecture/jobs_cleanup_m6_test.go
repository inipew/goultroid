package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestM6JobsCompatibilitySurfaceIsGone(t *testing.T) {
	root := repositoryRoot(t)
	fset := token.NewFileSet()

	for _, scanRoot := range []string{
		filepath.Join(root, "internal"),
		filepath.Join(root, "plugins"),
	} {
		err := filepath.WalkDir(scanRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}

			jobsAliases := map[string]struct{}{}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil || importPath != "github.com/inipew/goultroid/internal/jobs" {
					continue
				}
				alias := "jobs"
				if spec.Name != nil {
					alias = spec.Name.Name
				}
				if alias == "." {
					t.Errorf("dot import of internal/jobs bypasses compatibility fencing in %s", filepath.ToSlash(path))
					continue
				}
				if alias != "_" {
					jobsAliases[alias] = struct{}{}
				}
			}

			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok || selector.Sel == nil {
					return true
				}
				ident, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				if _, jobsImport := jobsAliases[ident.Name]; !jobsImport {
					return true
				}
				switch selector.Sel.Name {
				case "NewManager", "StorePortsFromStore", "Store":
					t.Errorf("obsolete jobs compatibility selector %s.%s remains in %s",
						ident.Name, selector.Sel.Name, filepath.ToSlash(path))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	managerRaw, err := os.ReadFile(filepath.Join(root, "internal", "jobs", "manager.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(managerRaw), "func NewManager(") {
		t.Fatal("obsolete jobs.NewManager compatibility constructor remains")
	}

	portsRaw, err := os.ReadFile(filepath.Join(root, "internal", "jobs", "store_ports.go"))
	if err != nil {
		t.Fatal(err)
	}
	portsSource := string(portsRaw)
	if strings.Contains(portsSource, "type Store interface") || strings.Contains(portsSource, "StorePortsFromStore") {
		t.Fatal("obsolete aggregate jobs.Store compatibility API remains")
	}
}
