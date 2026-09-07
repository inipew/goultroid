package core_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/settings"
)

// TestArchitecture_CoreLayerIsolation ensures internal/core remains pure and never imports higher layers.
func TestArchitecture_CoreLayerIsolation(t *testing.T) {
	coreDir := "."
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, coreDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse core package: %v", err)
	}

	disallowedPrefixes := []string{
		"github.com/inipew/goultroid/internal/app",
		"github.com/inipew/goultroid/internal/telegram",
		"github.com/inipew/goultroid/internal/services",
		"github.com/inipew/goultroid/internal/settings",
		"github.com/inipew/goultroid/internal/database",
		"github.com/inipew/goultroid/internal/ui",
		"github.com/inipew/goultroid/plugins",
	}

	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, disallowed := range disallowedPrefixes {
					if strings.HasPrefix(path, disallowed) {
						t.Errorf("Architecture violation: %s imports disallowed layer %s", filepath.Base(fileName), path)
					}
				}
			}
		}
	}
}

// TestArchitecture_SettingsLayerIsolation ensures internal/settings never imports plugins or telegram client.
func TestArchitecture_SettingsLayerIsolation(t *testing.T) {
	settingsDir := "../settings"
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, settingsDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse settings package: %v", err)
	}

	disallowedPrefixes := []string{
		"github.com/inipew/goultroid/plugins",
		"github.com/inipew/goultroid/internal/telegram",
		"github.com/inipew/goultroid/internal/app",
	}

	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, disallowed := range disallowedPrefixes {
					if strings.HasPrefix(path, disallowed) {
						t.Errorf("Architecture violation: %s imports disallowed layer %s", filepath.Base(fileName), path)
					}
				}
			}
		}
	}
}

// TestArchitecture_CallbackLayerIsolation ensures internal/services/callback never imports plugins or telegram client.
func TestArchitecture_CallbackLayerIsolation(t *testing.T) {
	callbackDir := "../services/callback"
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, callbackDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse callback package: %v", err)
	}

	disallowedPrefixes := []string{
		"github.com/inipew/goultroid/plugins",
		"github.com/inipew/goultroid/internal/telegram",
		"github.com/inipew/goultroid/internal/app",
	}

	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, disallowed := range disallowedPrefixes {
					if strings.HasPrefix(path, disallowed) {
						t.Errorf("Architecture violation: %s imports disallowed layer %s", filepath.Base(fileName), path)
					}
				}
			}
		}
	}
}

// TestArchitecture_ScopeInvariants verifies domain invariants for scopes.
func TestArchitecture_ScopeInvariants(t *testing.T) {
	// Invariant 1: Global scope must have ScopeID = 0
	g := settings.ScopeRef{Type: settings.ScopeGlobal, ID: 0}
	if err := g.Validate(); err != nil {
		t.Errorf("valid global scope rejected: %v", err)
	}
	badG := settings.ScopeRef{Type: settings.ScopeGlobal, ID: 100}
	if err := badG.Validate(); err == nil {
		t.Errorf("invalid global scope with non-zero ID allowed")
	}

	// Invariant 2: Chat scope must have non-zero ScopeID
	c := settings.ScopeRef{Type: settings.ScopeChat, ID: -100123}
	if err := c.Validate(); err != nil {
		t.Errorf("valid chat scope rejected: %v", err)
	}
	badC := settings.ScopeRef{Type: settings.ScopeChat, ID: 0}
	if err := badC.Validate(); err == nil {
		t.Errorf("invalid chat scope with zero ID allowed")
	}

	// Invariant 3: User scope must have non-zero ScopeID
	u := settings.ScopeRef{Type: settings.ScopeUser, ID: 12345}
	if err := u.Validate(); err != nil {
		t.Errorf("valid user scope rejected: %v", err)
	}
	badU := settings.ScopeRef{Type: settings.ScopeUser, ID: 0}
	if err := badU.Validate(); err == nil {
		t.Errorf("invalid user scope with zero ID allowed")
	}
}

// TestArchitecture_UILayerIsolation ensures internal/ui never imports disallowed layers (bug13 #40).
func TestArchitecture_UILayerIsolation(t *testing.T) {
	uiDir := "../ui"
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, uiDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse ui package: %v", err)
	}
	disallowedPrefixes := []string{
		"github.com/inipew/goultroid/internal/database",
		"github.com/inipew/goultroid/plugins",
		"github.com/inipew/goultroid/internal/scheduler",
	}
	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, disallowed := range disallowedPrefixes {
					if strings.HasPrefix(path, disallowed) {
						t.Errorf("Architecture violation: %s imports disallowed layer %s", filepath.Base(fileName), path)
					}
				}
			}
		}
	}
}

// TestArchitecture_DomainNoTGImport ensures domain-adjacent layers never import raw tg.* (bug13 #40).
func TestArchitecture_DomainNoTGImport(t *testing.T) {
	dirs := []string{"../settings", "../services/callback"}
	for _, dir := range dirs {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			for fileName, file := range pkg.Files {
				for _, imp := range file.Imports {
					path := strings.Trim(imp.Path.Value, `"`)
					if strings.Contains(path, "github.com/gotd/td/tg") {
						// callback/types.go legitimately needs tg for CallbackContext.Edit.
						// Forbid in settings and callback non-types files.
						base := filepath.Base(fileName)
						if base == "types.go" {
							continue
						}
						t.Errorf("Architecture violation: %s in %s imports tg directly", base, dir)
					}
				}
			}
		}
	}
}

// TestArchitecture_PluginNoDirectDB ensures plugins never do raw DB access (bug13 #40).
func TestArchitecture_PluginNoDirectDB(t *testing.T) {
	pluginsDir := "../../plugins"
	fset := token.NewFileSet()
	err := filepath.Walk(pluginsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(path, "_test.go") || !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read %s: %v", path, err)
		}
		content := string(src)
		// Heuristic: plugins should not call db.Exec / sql.DB directly beyond repository interfaces.
		if strings.Contains(content, "db.Exec(") && strings.Contains(content, "database/sql") {
			t.Errorf("Architecture violation: %s does raw db.Exec with database/sql", path)
		}
		_ = fset
		return nil
	})
	if err != nil {
		t.Fatalf("walk plugins failed: %v", err)
	}
}

// TestArchitecture_TelegramAdapterNoBusinessRule ensures internal/telegram does not import business domain (bug13 #40).
func TestArchitecture_TelegramAdapterNoBusinessRule(t *testing.T) {
	tgDir := "../telegram"
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, tgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse telegram package: %v", err)
	}
	disallowedPrefixes := []string{
		"github.com/inipew/goultroid/internal/settings",
	}
	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, disallowed := range disallowedPrefixes {
					if strings.HasPrefix(path, disallowed) {
						t.Errorf("Architecture violation: %s imports disallowed business layer %s", filepath.Base(fileName), path)
					}
				}
			}
		}
	}
}
