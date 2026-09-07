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
