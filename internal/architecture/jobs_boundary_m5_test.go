package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestM5PluginJobsBoundaryUsesNarrowClients(t *testing.T) {
	root := repositoryRoot(t)
	contextPath := filepath.Join(root, "internal", "plugin", "context.go")
	raw, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "Jobs() (JobClient, error)") {
		t.Fatal("PluginContext.Jobs no longer exposes JobClient")
	}
	if !strings.Contains(source, "Schedules() (ScheduleClient, error)") {
		t.Fatal("PluginContext.Schedules no longer exposes ScheduleClient")
	}
	if strings.Contains(source, "Jobs() (*jobs.Manager") || strings.Contains(source, "Schedules() (*jobs.Manager") {
		t.Fatal("PluginContext leaked concrete *jobs.Manager")
	}

	clientPath := filepath.Join(root, "internal", "plugin", "context_jobs.go")
	assertInterfaceMethods := func(typeName string, want []string) {
		t.Helper()
		got := interfaceMethodNames(t, clientPath, typeName)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("%s methods = %v, want %v", typeName, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s methods = %v, want %v", typeName, got, want)
			}
		}
	}
	assertInterfaceMethods("JobClient", []string{"Register", "RegisterHandler", "Trigger"})
	assertInterfaceMethods("ScheduleClient", []string{"DisableSchedule", "SaveSchedule"})
}

func TestM5ProductionPluginsDoNotDependOnJobsManager(t *testing.T) {
	root := repositoryRoot(t)
	pluginsDir := filepath.Join(root, "plugins")
	fset := token.NewFileSet()

	err := filepath.WalkDir(pluginsDir, func(path string, entry fs.DirEntry, walkErr error) error {
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
			jobsAliases[alias] = struct{}{}
		}
		if len(jobsAliases) == 0 {
			return nil
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel == nil || selector.Sel.Name != "Manager" {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if ok {
				if _, jobsImport := jobsAliases[ident.Name]; jobsImport {
					t.Errorf("production plugin %s depends on concrete jobs.Manager", filepath.ToSlash(path))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestM5JobsManagerUsesResponsibilityStorePorts(t *testing.T) {
	root := repositoryRoot(t)
	managerPath := filepath.Join(root, "internal", "jobs", "manager.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, managerPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	foundStores := false
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Manager" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				t.Fatal("jobs Manager is not a struct")
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if name.Name == "store" {
						t.Fatal("jobs Manager regained broad store field")
					}
					if name.Name == "stores" {
						ident, ok := field.Type.(*ast.Ident)
						if !ok || ident.Name != "StorePorts" {
							t.Fatalf("jobs Manager stores field type = %T, want StorePorts", field.Type)
						}
						foundStores = true
					}
				}
			}
		}
	}
	if !foundStores {
		t.Fatal("jobs Manager lost StorePorts ownership")
	}

	scheduleRaw, err := os.ReadFile(filepath.Join(root, "internal", "jobs", "manager_schedule.go"))
	if err != nil {
		t.Fatal(err)
	}
	scheduleSource := string(scheduleRaw)
	if !strings.Contains(scheduleSource, "m.stores.Schedules") {
		t.Fatal("manager_schedule.go no longer uses ScheduleStore port")
	}
	for _, forbidden := range []string{
		"m.stores.Definitions", "m.stores.Occurrences", "m.stores.Attempts",
		"m.stores.Recovery", "m.stores.Outbox", "m.stores.Diagnostics",
	} {
		if strings.Contains(scheduleSource, forbidden) {
			t.Fatalf("manager_schedule.go gained unrelated store dependency %s", forbidden)
		}
	}

	managerRaw, err := os.ReadFile(managerPath)
	if err != nil {
		t.Fatal(err)
	}
	managerSource := string(managerRaw)
	if !strings.Contains(managerSource, "func NewManagerWithPorts(") {
		t.Fatal("narrow store-port constructor missing")
	}
	if !strings.Contains(managerSource, "return NewManagerWithPorts(client, StorePortsFromStore(store), pump)") {
		t.Fatal("compatibility NewManager no longer delegates through StorePorts")
	}
}

func TestM5SchedulerPluginDoesNotRequestJobsCapability(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "plugins", "scheduler", "module.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "plugin.CapScheduler") {
		t.Fatal("scheduler plugin lost scheduler capability")
	}
	if strings.Contains(source, "plugin.CapJobs") {
		t.Fatal("scheduler plugin still requests unrelated jobs capability")
	}
}

func TestM5AppWiresJobsThroughStorePorts(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "app", "wiring_core.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "jobs.NewManagerWithPorts(") {
		t.Fatal("production app no longer wires jobs through StorePorts")
	}
	if strings.Contains(source, "jobs.NewManager(taskEngine") {
		t.Fatal("production app regressed to compatibility aggregate jobs constructor")
	}
}

