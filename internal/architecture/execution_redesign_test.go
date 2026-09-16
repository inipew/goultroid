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
