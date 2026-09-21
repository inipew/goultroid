package assistant

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLegacyAssistantCompatibilityStackRemoved is an architectural regression
// gate for the P6-C cutover. The deleted a1/menu compatibility stack must not
// silently re-enter production wiring while a2 is the canonical interaction
// path.
func TestLegacyAssistantCompatibilityStackRemoved(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))

	for _, rel := range []string{
		"internal/assistant/menu",
		"internal/assistant/presentation",
		"internal/assistant/callback",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err == nil {
			t.Fatalf("retired legacy Assistant package still exists: %s", rel)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", rel, err)
		}
	}

	forbidden := []string{
		"LegacyAssistantMenu",
		"LegacyMenuCompatibility",
		"RegisterTextHandler",
		"MenuInstanceStore",
		"internal/assistant/menu",
		"internal/assistant/presentation",
		"internal/assistant/callback",
	}

	for _, pattern := range []string{
		"internal/assistant/client/v2*.go",
		"plugins/myxl/assistant_v2*.go",
	} {
		matches, err := filepath.Glob(filepath.Join(repoRoot, pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(matches) != 0 {
			t.Fatalf("transitional interaction source filenames still exist for %s: %v", pattern, matches)
		}
	}

	scanRoots := []string{
		"internal/assistant",
		"internal/app",
		"internal/module",
		"plugins/myxl",
	}
	for _, relRoot := range scanRoots {
		root := filepath.Join(repoRoot, relRoot)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(raw)
			for _, token := range forbidden {
				if strings.Contains(text, token) {
					rel, _ := filepath.Rel(repoRoot, path)
					t.Errorf("retired Assistant compatibility token %q found in %s", token, rel)
				}
			}
			if (strings.HasPrefix(relRoot, "internal/assistant") || relRoot == "plugins/myxl") &&
				strings.Contains(text, "a1:") {
				rel, _ := filepath.Rel(repoRoot, path)
				t.Errorf("legacy a1 callback envelope found in %s", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", relRoot, err)
		}
	}
}
