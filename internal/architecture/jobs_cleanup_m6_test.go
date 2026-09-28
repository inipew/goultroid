package architecture

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestM6JobsCompatibilitySurfaceIsGone(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		filepath.Join(root, "internal", "jobs"),
		filepath.Join(root, "internal", "app"),
		filepath.Join(root, "internal", "plugin"),
		filepath.Join(root, "plugins"),
	} {
		err := filepath.WalkDir(path, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(filePath, ".go") {
				return nil
			}
			raw, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}
			source := string(raw)
			if strings.Contains(source, "StorePortsFromStore(") {
				t.Errorf("obsolete StorePortsFromStore caller remains in %s", filepath.ToSlash(filePath))
			}
			if strings.Contains(source, "jobs.NewManager(") {
				t.Errorf("obsolete jobs.NewManager caller remains in %s", filepath.ToSlash(filePath))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	managerRaw, err := os.ReadFile(filepath.Join(root, "internal", "jobs", "manager.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(managerRaw), "func NewManager(") {
		t.Fatal("obsolete jobs.NewManager compatibility constructor remains")
	}

	portsRaw, err := os.ReadFile(filepath.Join(root, "internal", "jobs", "store_ports.go"))
	if err != nil {
		t.Fatal(err)
	}
	portsSource := string(portsRaw)
	if strings.Contains(portsSource, "type Store interface") || strings.Contains(portsSource, "StorePortsFromStore") {
		t.Fatal("obsolete aggregate jobs.Store compatibility API remains")
	}
}
