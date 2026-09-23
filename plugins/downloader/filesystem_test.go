package downloader

import (
	"testing"

	"github.com/inipew/goultroid/internal/platform/filesystem"
)

func attachDownloaderTestFilesystem(t *testing.T, p *Plugin) {
	t.Helper()
	root := t.TempDir()
	manager, err := filesystem.NewManager(root+"/data", root+"/cache", root+"/tmp", nil)
	if err != nil {
		t.Fatalf("filesystem manager: %v", err)
	}
	p.files = manager.ForOwner("downloader")
}
