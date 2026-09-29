package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Keep this list paired with the semantic restart tests owned by each feature.
// A new declaration or version bump requires updating that feature's restart
// test and then intentionally updating this inventory.
func TestDurableFeatureInventory(t *testing.T) {
	root := filepath.Join("..", "..")
	want := map[string]string{
		"internal/assistant/shell/shell.go":      "3",
		"plugins/calculator/calculator.go":       "1",
		"plugins/myxl/assistant_interaction.go":  "1",
		"plugins/settings/native_interaction.go": "1",
	}
	proof := map[string]string{
		"internal/assistant/shell/shell.go":      "internal/assistant/client/durable_restart_test.go",
		"plugins/calculator/calculator.go":       "plugins/calculator/durable_restart_test.go",
		"plugins/myxl/assistant_interaction.go":  "plugins/myxl/durable_restart_test.go",
		"plugins/settings/native_interaction.go": "plugins/settings/durable_restart_test.go",
	}
	got := make(map[string]string)
	for _, dir := range []string{"internal", "plugins"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				field, ok := node.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				name, ok := field.Key.(*ast.Ident)
				if !ok || name.Name != "DurabilityVersion" {
					return true
				}
				value, ok := field.Value.(*ast.BasicLit)
				if !ok || value.Kind != token.STRING {
					t.Errorf("%s: durability version must be a string literal", path)
					return true
				}
				version, err := strconv.Unquote(value.Value)
				if err != nil {
					t.Errorf("%s: invalid durability version: %v", path, err)
					return true
				}
				if version != "" {
					rel, err := filepath.Rel(root, path)
					if err != nil {
						t.Errorf("%s: relative path: %v", path, err)
						return true
					}
					got[filepath.ToSlash(rel)] = version
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("durable declarations = %v; want %v; update the owning restart test before changing this inventory", got, want)
	}
	for declaration, testPath := range proof {
		if _, ok := want[declaration]; !ok {
			t.Errorf("proof for undeclared feature %s", declaration)
		}
		if _, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, testPath), nil, 0); err != nil {
			t.Errorf("%s: restart proof missing or invalid: %v", testPath, err)
		}
	}
}
