package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guard the new foundations while legacy jobs/scheduler adapters are migrated.
func TestExecutionRedesignFoundations(t *testing.T) {
	imports := collectImports(t, repositoryRoot(t))
	forbidden := map[string][]string{
		"tasks":      {"internal/telegram", "internal/jobs", "internal/workers", "internal/taskengine", "internal/app", "plugins"},
		"admission":  {"internal/telegram", "internal/jobs", "internal/workers", "internal/taskengine", "internal/app", "plugins"},
		"workers":    {"internal/telegram", "internal/jobs", "internal/scheduler", "internal/admission", "internal/taskengine", "internal/app", "plugins"},
		"taskengine": {"internal/telegram", "internal/jobs", "internal/app", "plugins"},
		"scheduler":  {"internal/telegram", "internal/app", "plugins"},
	}
	for pkg, paths := range forbidden {
		for dep := range imports[modulePath+"/internal/"+pkg] {
			if (pkg != "scheduler" && dep == "database/sql") || strings.HasPrefix(dep, "github.com/gotd/") || strings.HasPrefix(dep, "modernc.org/sqlite") {
				t.Errorf("%s imports infrastructure %s", pkg, dep)
			}
			for _, path := range paths {
				prefix := modulePath + "/" + path
				if dep == prefix || strings.HasPrefix(dep, prefix+"/") {
					t.Errorf("%s imports forbidden dependency %s", pkg, dep)
				}
			}
		}
	}
}

func TestLegacyExecutionPackagesAreRemoved(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		"internal/tasks/manager.go",
		"internal/tasks/task.go",
		"internal/jobs/job.go",
		"internal/jobs/repository.go",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			t.Errorf("legacy execution implementation remains: %s", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "internal/workers"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read legacy workers directory: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			t.Errorf("legacy execution implementation remains: internal/workers/%s", entry.Name())
		}
	}
}

func TestArchitecture_ResourceCommandsGateInvariants(t *testing.T) {
	root := repositoryRoot(t)

	// 1. Check telegram dispatcher uses submitInteractiveCommand for all interactive commands
	dispDispatchPath := filepath.Join(root, "internal", "telegram", "dispatcher_dispatch.go")
	dispContent, err := os.ReadFile(dispDispatchPath)
	if err != nil {
		t.Fatalf("read %s: %v", dispDispatchPath, err)
	}
	if !strings.Contains(string(dispContent), "d.submitInteractiveCommand") {
		t.Errorf("%s must invoke submitInteractiveCommand to route commands via TaskEngine", dispDispatchPath)
	}

	// 2. Check assistant router guards resources with ErrTasksNotConfigured
	asstRouterPath := filepath.Join(root, "internal", "assistant", "command", "router.go")
	asstContent, err := os.ReadFile(asstRouterPath)
	if err != nil {
		t.Fatalf("read %s: %v", asstRouterPath, err)
	}
	if !strings.Contains(string(asstContent), "if len(cmd.Resources) > 0") || !strings.Contains(string(asstContent), "ErrTasksNotConfigured") {
		t.Errorf("%s must guard resource-bearing commands with ErrTasksNotConfigured when task engine is unconfigured", asstRouterPath)
	}

	// 3. Check scheduled_action.go guards command.Resources with tasks client requirement
	schedActionPath := filepath.Join(root, "internal", "app", "scheduled_action.go")
	schedContent, err := os.ReadFile(schedActionPath)
	if err != nil {
		t.Fatalf("read %s: %v", schedActionPath, err)
	}
	if !strings.Contains(string(schedContent), "if len(command.Resources) == 0") || !strings.Contains(string(schedContent), "h.tasks == nil") {
		t.Errorf("%s must enforce TaskEngine client for resource-bearing scheduled commands", schedActionPath)
	}
}
