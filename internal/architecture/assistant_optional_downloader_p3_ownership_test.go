package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP3DownloaderNativePipelineHasConcreteRetainedOwnershipAcceptance(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "plugins", "downloader", "url_pipeline.go"): {
			"p.registerRetainedAsset(taskCtx, targetStore, asset, producer)",
		},
		filepath.Join(root, "plugins", "downloader", "url_progressive_ownership_test.go"): {
			"TestP3NativeURLPipelineRegistersRetainedOwnership",
			"ownership.Asset(context.Background(), physical.assetID)",
			"mediaregistry.LifecycleRetained",
			"downloaderRegistryOwner",
		},
	}
	for path, required := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, invariant := range required {
			if !strings.Contains(source, invariant) {
				t.Fatalf("P3 retained-ownership invariant missing from %s: %q", path, invariant)
			}
		}
	}
}
