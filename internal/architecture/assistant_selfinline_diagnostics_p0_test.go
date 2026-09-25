package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP0SelfInlineDiagnosticsNormalizeAfterSharedTransport(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "presentation", "selfinline", "render.go"): {
			"normalizeQueryError(err)",
			"normalizeSendError(err)",
		},
		filepath.Join(root, "internal", "presentation", "selfinline", "diagnostics.go"): {
			"tg.IsBotResponseTimeout(err)",
			"tg.IsBotInvalid(err)",
			"tg.IsChatSendInlineForbidden(err)",
			"tg.IsInlineResultExpired(err)",
			"tg.IsQueryIDInvalid(err)",
			"tg.IsResultIDInvalid(err)",
			"func (e *diagnosticError) Unwrap() error",
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
				t.Fatalf("self-inline diagnostic invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP0SelfInlineDiagnosticsDoNotAddRetryAuthority(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "presentation", "selfinline", "diagnostics.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []string{
		"RetryRPC(",
		"NewRPCExecutor(",
		"time.Sleep(",
		"time.NewTimer(",
		"time.NewTicker(",
		"go func(",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("self-inline diagnostics introduced retry/background authority %q", forbidden)
		}
	}
}
