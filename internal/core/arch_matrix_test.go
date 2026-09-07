package core_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestArchitecture_DependencyMatrix enforces the explicit dependency graph from bug13.2 final plan.
// It ensures architecture regressions fail CI, not just developer discipline.
func TestArchitecture_DependencyMatrix(t *testing.T) {
	// Define allowed imports per package (relative to internal/) — reflects current architecture, not ideal future.
	// Goal: prevent regression (e.g., core -> app/telegram, ui -> database, plugin -> raw DB).
	rules := map[string][]string{
		"core":                 {}, // core should not import app, telegram, database, ui, plugins, services, settings
		"settings":             {"github.com/inipew/goultroid/internal/core", "github.com/inipew/goultroid/internal/database"},
		"services/callback":    {"github.com/inipew/goultroid/internal/core", "github.com/inipew/goultroid/internal/services/ratelimit"},
		"services/interaction": {"github.com/inipew/goultroid/internal/core", "github.com/inipew/goultroid/internal/ui"},
		"telegram":             {"github.com/inipew/goultroid/internal/core", "github.com/inipew/goultroid/internal/domain/peer", "github.com/inipew/goultroid/internal/database", "github.com/inipew/goultroid/internal/services/callback", "github.com/inipew/goultroid/internal/services/inline"},
		"ui":                   {"github.com/inipew/goultroid/internal/core", "github.com/inipew/goultroid/internal/services/interaction/navigation", "github.com/inipew/goultroid/internal/services/callback"},
	}

	base := ".."
	for pkg, allowedPrefixes := range rules {
		dir := filepath.Join(base, pkg)
		if _, err := os.Stat(dir); err != nil {
			continue // package may not exist yet
		}
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", pkg, err)
		}
		disallowed := []string{
			"github.com/inipew/goultroid/internal/app",
			"github.com/inipew/goultroid/internal/telegram",
			"github.com/inipew/goultroid/internal/database",
			"github.com/inipew/goultroid/internal/ui",
			"github.com/inipew/goultroid/plugins",
			"github.com/inipew/goultroid/internal/services",
			"github.com/inipew/goultroid/internal/settings",
		}
		// For core, all disallowed are forbidden
		// For others, only check that they don't import disallowed that aren't in allowed
		for _, pkgFiles := range pkgs {
			for fname, f := range pkgFiles.Files {
				for _, imp := range f.Imports {
					path := strings.Trim(imp.Path.Value, `"`)
					// Check if import is allowed for this pkg
					allowed := false
					for _, a := range allowedPrefixes {
						if path == a || strings.HasPrefix(path, a+"/") {
							allowed = true
							break
						}
					}
					// If path is disallowed and not explicitly allowed, fail
					for _, d := range disallowed {
						if strings.HasPrefix(path, d) && !allowed {
							// Special case: ui may import ui/render, services may import core, etc. — but not app/database
							if pkg == "core" && strings.HasPrefix(path, "github.com/gotd/td/tg") {
								// Currently core still imports tg for TelegramServicer; allowed for now with deprecation, but flag as warning
								continue
							}
							t.Errorf("arch violation: %s/%s imports %s (not in allowed %v)", pkg, filepath.Base(fname), path, allowedPrefixes)
						}
					}
				}
			}
		}
	}
}
