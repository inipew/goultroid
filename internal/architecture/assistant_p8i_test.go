package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8ICombinedAcceptanceCoversCanonicalAuthorities(t *testing.T) {
	root := repositoryRoot(t)

	raw, err := os.ReadFile(filepath.Join(root, "internal", "assistant", "p8i_resource_acceptance_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"p8iInlineQueries   = 10000",
		"p8iCallbackBurst   = 512",
		"calculator.New()",
		"wikipedia.New()",
		"downloader.New()",
		`ResourceCapacities: map[string]int64{"download": 1, "process": 1}`,
		"taskRuntime.CancelScope(downloaderScope, tasks.CauseScopeClosed)",
		"rootinteraction.ErrCapacity",
		"telegram.NewInMemoryRPCMetrics()",
		"p8iSampleProcess()",
		"restartedSessions.ResolveCallback",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("P8-I acceptance invariant missing: %q", required)
		}
	}

	diagnosticsRaw, err := os.ReadFile(filepath.Join(root, "internal", "services", "inline", "diagnostics.go"))
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := string(diagnosticsRaw)
	for _, required := range []string{
		"e.cache.Len()",
		"e.cache.RetainedBytes()",
	} {
		if !strings.Contains(diagnostics, required) {
			t.Fatalf("P8-I inline diagnostics must read canonical cache state: %q", required)
		}
	}

	for _, forbidden := range []string{
		"NewTaskEngine(",
		"NewInteractionRuntime(",
		"map[int64]*",
		"time.NewTicker(",
		"time.Tick(",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("P8-I introduced forbidden acceptance runtime/state pattern %q", forbidden)
		}
	}
}
