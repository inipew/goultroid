package architecture

import (
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
	}
	for pkg, paths := range forbidden {
		for dep := range imports[modulePath+"/internal/"+pkg] {
			if dep == "database/sql" || strings.HasPrefix(dep, "github.com/gotd/") || strings.HasPrefix(dep, "modernc.org/sqlite") {
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
