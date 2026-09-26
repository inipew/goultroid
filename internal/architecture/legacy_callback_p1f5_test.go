package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1F5LegacyCallbackFinalProductionAcceptance(t *testing.T) {
	root := repositoryRoot(t)
	legacyImport := modulePath + "/internal/services/callback"
	forbidden := []string{
		"callback.Router",
		"callback.Handler",
		"callback.HandlerWithOptions",
		"callback.CallbackContext",
		"callback.StateStore",
		"callback.ScopedCallbackStore",
		"callback.StateWriter",
		"callback.StateScope",
		"SetCallbackRegistrar(",
		"SetCallbackRouter(",
		"getCallbackRouter(",
		"SetStateStore(",
		"RequiresCallbackState",
		"EncodeCallbackData(",
		"EncodeCallbackDataChecked(",
		"ParseCallbackData(",
		"CallbackVersion1",
		"CoreCallbackDispatcher",
		"dispatchCoreCallback(",
		"callbackRouter",
		"callbackCleanups",
	}

	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, "\"") == legacyImport {
				t.Errorf("P1-F5 legacy callback package import remains in production: %s", rel)
			}
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(raw)
		for _, marker := range forbidden {
			if strings.Contains(source, marker) {
				t.Errorf("P1-F5 legacy callback marker %q remains in production: %s", marker, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	callbackDir := filepath.Join(root, "internal", "services", "callback")
	if _, err := os.Stat(callbackDir); err == nil {
		t.Fatal("P1-F5 legacy callback package directory still exists")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestP1F5SettingsAndMyXLRemainZeroLegacyCallback(t *testing.T) {
	root := repositoryRoot(t)
	for _, plugin := range []string{"settings", "myxl"} {
		dir := filepath.Join(root, "plugins", plugin)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			source := string(raw)
			for _, marker := range []string{
				"/internal/services/callback",
				"callback.",
				"ScopedCallbackStore",
				"SetStateStore",
				"RequiresCallbackState",
				"EncodeCallbackData(",
				"ParseCallbackData(",
				"v1:" + plugin,
			} {
				if strings.Contains(source, marker) {
					t.Errorf("%s/%s reintroduced legacy callback surface %q", plugin, entry.Name(), marker)
				}
			}
		}
	}
}

func TestP1F5InteractionRuntimeOwnsNoIdleWorker(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "interaction", "runtime.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if _, ok := node.(*ast.GoStmt); ok {
			t.Error("interaction.Runtime must not acquire a background goroutine")
		}
		return true
	})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, marker := range []string{"time.NewTicker(", "time.Tick(", "time.AfterFunc("} {
		if strings.Contains(source, marker) {
			t.Errorf("interaction.Runtime acquired idle timer ownership %q", marker)
		}
	}
}
