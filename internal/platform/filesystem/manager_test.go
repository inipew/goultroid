package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inipew/goultroid/internal/resource"
)

func TestFilesystemManager_ScopedDirsAndSafePath(t *testing.T) {
	tmpDir := t.TempDir()
	rm := resource.NewManager()

	mgr, err := NewManager(filepath.Join(tmpDir, "data"), filepath.Join(tmpDir, "cache"), filepath.Join(tmpDir, "tmp"), rm)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	pluginDir, err := mgr.PluginDataDir("afk")
	if err != nil {
		t.Fatalf("PluginDataDir failed: %v", err)
	}

	// SafePath valid child
	safe, err := mgr.SafePath(pluginDir, "state.json")
	if err != nil || safe != filepath.Join(pluginDir, "state.json") {
		t.Errorf("expected valid safe path, got: %s, err: %v", safe, err)
	}

	// SafePath directory traversal attack
	_, err = mgr.SafePath(pluginDir, "../../../etc/passwd")
	if err == nil {
		t.Errorf("expected directory traversal error, got nil")
	}
}

func TestFilesystemManager_TempFileLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	rm := resource.NewManager()

	mgr, err := NewManager(tmpDir, "", "", rm)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	f, err := mgr.CreateTempFile("downloader", "test-*.bin")
	if err != nil {
		t.Fatalf("CreateTempFile failed: %v", err)
	}
	name := f.Name()
	f.Close()

	// Verify tracked in ResourceManager
	res := rm.ByOwner("plugin:downloader")
	if len(res) != 1 || res[0].ID != name {
		t.Errorf("expected temp file tracked in manager, got: %+v", res)
	}

	// Remove temp file
	if err := mgr.RemoveTempFile(name); err != nil {
		t.Fatalf("RemoveTempFile failed: %v", err)
	}

	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("expected file to be deleted from disk")
	}

	// Verify released from ResourceManager
	res = rm.ByOwner("plugin:downloader")
	if len(res) != 0 {
		t.Errorf("expected temp file released from manager, got: %+v", res)
	}
}

func TestFilesystemScopeRejectsAnotherOwnersPath(t *testing.T) {
	mgr, err := NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := mgr.ForOwner("a")
	ownerB := mgr.ForOwner("b")
	path, err := ownerA.CreateTempDir("owned-*")
	if err != nil {
		t.Fatal(err)
	}
	defer ownerA.RemoveTempDir(path)
	if err := ownerB.RemoveTempDir(path); err == nil {
		t.Fatal("expected owner-bound filesystem scope to reject another owner's path")
	}
}
