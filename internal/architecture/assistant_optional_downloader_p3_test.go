package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP3DownloaderURLProgressiveEnhancementUsesOneSharedPipeline(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "plugins", "downloader", "downloader.go"): {
			"selfinline.FallbackSafe(err)",
			"startNativeURLDownload",
		},
		filepath.Join(root, "plugins", "downloader", "delivery.go"): {
			"return p.submitURLPipeline(",
			"urlDownloadRequest{",
			"urlPipelineHooks{",
		},
		filepath.Join(root, "plugins", "downloader", "url_native.go"): {
			"Mode:      download.MediaModeDefault",
			"Format:    download.MediaFormatDefault",
			"MaxHeight: 0",
			"callCtx.SendMedia(",
			"p.submitURLPipeline(",
		},
		filepath.Join(root, "plugins", "downloader", "url_pipeline.go"): {
			"Resources:        p.urlResources(request.URL)",
			"p.registry.Download(taskCtx, request.URL, targetStore",
			"p.registerRetainedAsset(taskCtx, targetStore, asset, producer)",
			"p.submitRetainedDelivery(",
			"request.MaxHeight",
		},
		filepath.Join(root, "plugins", "downloader", "url_progressive_test.go"): {
			"TestP3NativeURLFallbackUsesDefaultSharedPipelineAndResourceHandoff",
			"TestP3SafeSelfInlineFailureFallsBackToNativeURLPipeline",
			"TestP3SendStageFailureDoesNotStartNativeDuplicate",
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
				t.Fatalf("P3 downloader progressive-enhancement invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP3DownloaderURLPipelineAddsNoSecondExecutionAuthority(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("plugins", "downloader", "url_pipeline.go"),
		filepath.Join("plugins", "downloader", "url_native.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{
			"taskengine.New",
			"download.NewRegistry(",
			"telegram.NewRPCExecutor",
			"go func(",
			"time.NewTicker(",
			"time.AfterFunc(",
			"sync.Map",
		} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("P3 downloader introduced duplicate execution/state mechanism %q in %s", forbidden, rel)
			}
		}
	}
}
