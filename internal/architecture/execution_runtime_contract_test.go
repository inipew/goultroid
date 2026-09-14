package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestExecutionRuntimeModelHasNoPersistedClosures(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string]map[string]struct{}{
		filepath.Join(root, "internal", "tasks", "model_v2.go"): {
			"WorkSpec": {},
		},
		filepath.Join(root, "internal", "tasks", "result_v2.go"): {
			"TaskResult":   {},
			"TaskSnapshot": {},
		},
		filepath.Join(root, "internal", "jobs", "model_v2.go"): {
			"JobDefinition": {},
			"JobSchedule":   {},
			"JobOccurrence": {},
			"JobAttempt":    {},
		},
	}

	for path, typeNames := range checks {
		assertValueTypesContainNoRuntimeFields(t, path, typeNames)
	}
}

func TestExecutionRuntimeNewFilesRespectDependencyBoundary(t *testing.T) {
	root := repositoryRoot(t)
	files := []string{
		filepath.Join(root, "internal", "tasks", "model_v2.go"),
		filepath.Join(root, "internal", "tasks", "result_v2.go"),
		filepath.Join(root, "internal", "tasks", "contracts_v2.go"),
		filepath.Join(root, "internal", "jobs", "model_v2.go"),
		filepath.Join(root, "internal", "jobs", "payload_registry_v2.go"),
		filepath.Join(root, "internal", "jobs", "contracts_v2.go"),
		filepath.Join(root, "internal", "taskengine", "config.go"),
		filepath.Join(root, "internal", "taskengine", "catalog.go"),
	}
	forbidden := []string{
		modulePath + "/internal/app",
		modulePath + "/internal/database",
		modulePath + "/internal/plugin",
		modulePath + "/internal/scheduler",
		modulePath + "/internal/telegram",
		modulePath + "/plugins/",
	}

	for _, path := range files {
		for _, dep := range importsInFile(t, path) {
			for _, prefix := range forbidden {
				if dep == prefix || strings.HasPrefix(dep, prefix) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s must not import execution outer layer %s", rel, dep)
				}
			}
		}
	}
}

func assertValueTypesContainNoRuntimeFields(t *testing.T, path string, typeNames map[string]struct{}) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	seen := make(map[string]bool, len(typeNames))
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, rawSpec := range gen.Specs {
			typeSpec, ok := rawSpec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, wanted := typeNames[typeSpec.Name.Name]; !wanted {
				continue
			}
			seen[typeSpec.Name.Name] = true
			ast.Inspect(typeSpec.Type, func(node ast.Node) bool {
				switch node.(type) {
				case *ast.FuncType, *ast.ChanType, *ast.InterfaceType:
					t.Errorf("%s.%s contains runtime-capability field %T", filepath.Base(path), typeSpec.Name.Name, node)
				}
				return true
			})
		}
	}
	for name := range typeNames {
		if !seen[name] {
			t.Errorf("%s: expected type %s was not found", filepath.Base(path), name)
		}
	}
}

func importsInFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse imports in %s: %v", path, err)
	}
	imports := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("unquote import %s: %v", spec.Path.Value, err)
		}
		imports = append(imports, path)
	}
	return imports
}
