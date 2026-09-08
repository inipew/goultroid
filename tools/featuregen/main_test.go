package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscover_RealPlugins(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("failed to get repo root: %v", err)
	}

	pluginsDir := filepath.Join(root, "plugins")
	entries, err := discover(pluginsDir, root)
	if err != nil {
		t.Fatalf("discover failed on real plugins: %v", err)
	}

	if len(entries) < 25 {
		t.Fatalf("expected at least 25 plugins, got %d", len(entries))
	}

	for i := 1; i < len(entries); i++ {
		if entries[i-1].importPath >= entries[i].importPath {
			t.Errorf("entries are not strictly sorted: %s >= %s", entries[i-1].importPath, entries[i].importPath)
		}
	}

	gen := generate(entries)
	if !strings.Contains(gen, "var builtinModules = []module.Module{") {
		t.Errorf("generated output missing builtinModules declaration")
	}
}

func TestDiscover_MissingVarModule(t *testing.T) {
	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "myplugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	code := `package myplugin
func Something() {}
`
	if err := os.WriteFile(filepath.Join(pluginDir, "module.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := discover(tmp, tmp)
	if err == nil || !strings.Contains(err.Error(), "must declare package-level 'var Module'") {
		t.Fatalf("expected missing var Module error, got: %v", err)
	}
}

func TestDiscover_MissingManifest(t *testing.T) {
	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "myplugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	code := `package myplugin

import (
	"context"
	"github.com/inipew/goultroid/internal/module"
)

type ModuleType struct{}
var Module ModuleType

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	return nil
}
`
	if err := os.WriteFile(filepath.Join(pluginDir, "module.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := discover(tmp, tmp)
	if err == nil || !strings.Contains(err.Error(), "must implement Manifest()") {
		t.Fatalf("expected missing Manifest error, got: %v", err)
	}
}

func TestDiscover_MissingRegister(t *testing.T) {
	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "myplugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	code := `package myplugin

import (
	"github.com/inipew/goultroid/internal/module"
)

type ModuleType struct{}
var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID: "myplugin",
	}
}
`
	if err := os.WriteFile(filepath.Join(pluginDir, "module.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := discover(tmp, tmp)
	if err == nil || !strings.Contains(err.Error(), "must implement Register(") {
		t.Fatalf("expected missing Register error, got: %v", err)
	}
}

func TestDiscover_InvalidManifestID(t *testing.T) {
	tmp := t.TempDir()
	pluginDir := filepath.Join(tmp, "myplugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	code := `package myplugin

import (
	"context"
	"github.com/inipew/goultroid/internal/module"
)

type ModuleType struct{}
var Module ModuleType

func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{
		ID: "INVALID ID WITH SPACES",
	}
}

func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error {
	return nil
}
`
	if err := os.WriteFile(filepath.Join(pluginDir, "module.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := discover(tmp, tmp)
	if err == nil || !strings.Contains(err.Error(), "invalid module ID") {
		t.Fatalf("expected invalid module ID error, got: %v", err)
	}
}

func TestDiscover_DuplicateModuleID(t *testing.T) {
	tmp := t.TempDir()
	p1 := filepath.Join(tmp, "plugina")
	p2 := filepath.Join(tmp, "pluginb")
	if err := os.MkdirAll(p1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p2, 0o755); err != nil {
		t.Fatal(err)
	}

	code1 := `package plugina
import (
	"context"
	"github.com/inipew/goultroid/internal/module"
)
type ModuleType struct{}
var Module ModuleType
func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{ID: "duplicate-id"}
}
func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error { return nil }
`
	code2 := `package pluginb
import (
	"context"
	"github.com/inipew/goultroid/internal/module"
)
type ModuleType struct{}
var Module ModuleType
func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{ID: "duplicate-id"}
}
func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error { return nil }
`
	if err := os.WriteFile(filepath.Join(p1, "module.go"), []byte(code1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p2, "module.go"), []byte(code2), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := discover(tmp, tmp)
	if err == nil || !strings.Contains(err.Error(), "duplicate module ID") {
		t.Fatalf("expected duplicate module ID error, got: %v", err)
	}
}

func TestDiscover_DuplicateAlias(t *testing.T) {
	tmp := t.TempDir()
	p1 := filepath.Join(tmp, "nested1", "plugina")
	p2 := filepath.Join(tmp, "nested2", "plugina")
	if err := os.MkdirAll(p1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p2, 0o755); err != nil {
		t.Fatal(err)
	}

	code1 := `package plugina
import (
	"context"
	"github.com/inipew/goultroid/internal/module"
)
type ModuleType struct{}
var Module ModuleType
func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{ID: "id-a"}
}
func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error { return nil }
`
	code2 := `package plugina
import (
	"context"
	"github.com/inipew/goultroid/internal/module"
)
type ModuleType struct{}
var Module ModuleType
func (ModuleType) Manifest() module.Manifest {
	return module.Manifest{ID: "id-b"}
}
func (ModuleType) Register(ctx context.Context, rt *module.Runtime) error { return nil }
`
	if err := os.WriteFile(filepath.Join(p1, "module.go"), []byte(code1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p2, "module.go"), []byte(code2), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := discover(tmp, tmp)
	if err == nil || !strings.Contains(err.Error(), "duplicate generated import alias") {
		t.Fatalf("expected duplicate alias error, got: %v", err)
	}
}
