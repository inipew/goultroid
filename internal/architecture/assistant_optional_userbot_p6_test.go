package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type p6ClosureCell struct {
	feature   string
	condition string
	path      string
	test      string
}

func TestP6AssistantOptionalUserbotClosureMatrix(t *testing.T) {
	root := repositoryRoot(t)
	cells := []p6ClosureCell{
		{feature: "composition", condition: "Assistant absent", path: "internal/app/selfinline_optional_test.go", test: "TestP0BAssistantAbsenceIsSupportedComposition"},
		{feature: "Help", condition: "Assistant absent", path: "plugins/help/help_test.go", test: "TestHelpUserbotFallsBackToNativeWithoutRenderer"},
		{feature: "Help", condition: "inline disabled/query/select failure", path: "plugins/help/help_test.go", test: "TestHelpUserbotFallsBackAfterSafeSelfInlineFailure"},
		{feature: "Help", condition: "send-stage ambiguity", path: "plugins/help/help_test.go", test: "TestHelpUserbotSendStageFailureDoesNotEmitNativeDuplicate"},
		{feature: "Calculator", condition: "Assistant absent", path: "plugins/calculator/calculator_test.go", test: "TestCalculatorCommandFallsBackToNativeWithoutRenderer"},
		{feature: "Calculator", condition: "inline disabled/query/select failure", path: "plugins/calculator/calculator_test.go", test: "TestCalculatorCommandFallsBackAfterSafeSelfInlineFailure"},
		{feature: "Calculator", condition: "send-stage ambiguity", path: "plugins/calculator/calculator_test.go", test: "TestCalculatorCommandSendStageFailureDoesNotEmitNativeDuplicate"},
		{feature: "Calculator", condition: "Assistant not ready then ready", path: "internal/app/selfinline_identity_test.go", test: "TestP2CalculatorFallsBackUntilAssistantIdentityIsReady"},
		{feature: "Calculator", condition: "Assistant ready then stopped", path: "internal/app/assistant_optional_closure_test.go", test: "TestP6CalculatorFallsBackAfterAssistantStops"},
		{feature: "self-inline", condition: "inline disabled preflight", path: "internal/app/selfinline_identity_test.go", test: "TestP0SelfInlineMapsAssistantCapabilityPreflightBeforeTransport"},
		{feature: "Downloader", condition: "Assistant absent/default URL", path: "plugins/downloader/url_progressive_test.go", test: "TestP3NativeURLFallbackUsesDefaultSharedPipelineAndResourceHandoff"},
		{feature: "Downloader", condition: "inline disabled/query/select failure", path: "plugins/downloader/url_progressive_test.go", test: "TestP3SafeSelfInlineFailureFallsBackToNativeURLPipeline"},
		{feature: "Downloader", condition: "send-stage ambiguity", path: "plugins/downloader/url_progressive_test.go", test: "TestP3SendStageFailureDoesNotStartNativeDuplicate"},
		{feature: "Downloader", condition: "plugin disable/reload", path: "internal/assistant/client/selfinline_downloader_reload_e2e_test.go", test: "TestSelfInlineDownloaderDisableEnableRejectsOldGenerationAndRebindsNew"},
		{feature: "Settings", condition: "native self-contained navigation", path: "plugins/settings/assistant_optional_test.go", test: "TestP4NativeSettingsNavigationOwnsAllCallbacks"},
		{feature: "MyXL", condition: "native userbot reference", path: "plugins/myxl/assistant_optional_test.go", test: "TestP6MyXLUserbotMenuDoesNotRequireAssistant"},
		{feature: "Wikipedia", condition: "native command reference", path: "plugins/wikipedia/assistant_optional_test.go", test: "TestP6WikipediaNativeCommandDoesNotRequireInline"},
		{feature: "Wikipedia", condition: "separate inline enhancement", path: "plugins/wikipedia/wikipedia_test.go", test: "TestWikipediaFeatureSpecOwnsPublicInlineLookup"},
		{feature: "Assistant identity", condition: "restart/failed never exposes stale identity", path: "internal/assistant/client/identity_p1_lifecycle_test.go", test: "TestP1AssistantRestartNeverExposesStaleInlineUsername"},
		{feature: "Assistant", condition: "shutdown quiesce closes admission", path: "internal/assistant/client/client_lifecycle_test.go", test: "TestAssistantClientQuiesceStopsAdmissionWithoutStoppingTransport"},
		{feature: "plugin runtime", condition: "cross-surface unload/reload generation fence", path: "internal/plugin/p8h_cross_surface_test.go", test: "TestP8HCrossSurfaceUnloadReloadGenerationAcceptance"},
		{feature: "plugin runtime", condition: "shutdown removes commands and is idempotent", path: "internal/plugin/manager_lifecycle_test.go", test: "TestManager_ShutdownIsReverseOrderAndIdempotent"},
	}

	for _, cell := range cells {
		cell := cell
		t.Run(cell.feature+"/"+cell.condition, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(cell.path)))
			if err != nil {
				t.Fatal(err)
			}
			needle := "func " + cell.test + "("
			if !strings.Contains(string(raw), needle) {
				t.Fatalf("closure cell is open: %s / %s requires %s in %s", cell.feature, cell.condition, cell.test, cell.path)
			}
		})
	}
}

func TestP6AssistantOptionalClosureKeepsP5RepoWideFence(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "architecture", "assistant_optional_userbot_p5_test.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, invariant := range []string{
		"TestP5SurfaceUserbotCommandsHaveNoUnfencedAssistantDependency",
		"commands with unspecified Surfaces as SurfaceUserbot",
		"userbot package %q still emits Assistant-owned callback navigation",
		"userbot package %q commands=%v gained Assistant/self-inline dependency without P5 fallback contract",
	} {
		if !strings.Contains(source, invariant) {
			t.Fatalf("P6 closure lost repo-wide P5 invariant %q", invariant)
		}
	}
}
