package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8EDownloaderUsesA2PreparedActionsAndSharedResources(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "plugins", "downloader", "interactive.go"): {
			"FeatureSpec() feature.Spec",
			"feature.InteractionInline",
			"feature.InteractionAction",
			"feature.SudoPolicy(execution.SurfaceInline)",
			"InlineBindings() []inlineservice.Binding",
			"rt.Engine.RegisterPreparedAction(",
			"rootinteraction.ActionAdmission{",
			"actionVideo720",
			"actionFormatOpus",
			"MaxHeight int",
			"ctx.Transition(encoded, interactiveTTL, runningView(state))",
			"p.submitInteractivePipeline(",
			"ctx.Cancel()",
		},
		filepath.Join(root, "plugins", "downloader", "delivery.go"): {
			"return p.submitURLPipeline(",
			"urlDownloadRequest{",
			"urlPipelineHooks{",
		},
		filepath.Join(root, "plugins", "downloader", "url_pipeline.go"): {
			"Resources:        p.urlResources(request.URL)",
			"p.registry.Download(taskCtx, request.URL, targetStore",
			"MaxHeight: request.MaxHeight",
			"p.registerRetainedAsset(taskCtx, targetStore, asset, producer)",
			"p.submitRetainedDelivery(",
		},
		filepath.Join(root, "plugins", "downloader", "downloader.go"): {
			"resources := []tasks.ResourceRequirement{{Name: \"download\", Amount: 1}}",
			"provider.Name() == \"extractor\"",
			"tasks.ResourceRequirement{Name: \"process\", Amount: 1}",
			"SetSelfInlineRenderer(renderer selfinline.Renderer)",
			"Query:    \"dl \" + normalized",
		},
		filepath.Join(root, "internal", "services", "download", "provider_extractor.go"): {
			"extractorSelectionArgs(opts)",
			"validVideoMaxHeight",
			"extractorResultTemplate",
			"tasks.HasHeldResource(ctx, \"process\")",
			"defer func() {",
			"os.RemoveAll(tmpDir)",
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
				t.Errorf("P8-E invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP8EDownloaderAddsNoSecondRuntimeOrUnboundedFeatureState(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "plugins", "downloader", "interactive.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []string{
		"go func(",
		"time.NewTicker(",
		"time.Tick(",
		"time.AfterFunc(",
		"sync.Map",
		"map[string]",
		"tasks.New",
		"telegram.NewRPCExecutor",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("P8-E downloader introduced forbidden runtime/state %q", forbidden)
		}
	}
	for _, required := range []string{
		"maxInteractiveURLLen = 2048",
		"maxInteractiveState  = 2304",
		"CachePolicy() inlineservice.CachePolicy",
		"return inlineservice.CacheNone",
		"Private:   true",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("P8-E bounded-state invariant missing: %q", required)
		}
	}
}
