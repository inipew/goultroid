package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8JFinalClosureKeepsAllPhaseFences(t *testing.T) {
	root := repositoryRoot(t)

	for _, rel := range []string{
		"internal/assistant/legacy_stack_test.go",
		"internal/architecture/assistant_p8b_test.go",
		"internal/architecture/assistant_p8c_test.go",
		"internal/architecture/assistant_p8d_test.go",
		"internal/architecture/assistant_p8e_test.go",
		"internal/architecture/assistant_p8f_test.go",
		"internal/architecture/assistant_p8g_reclamation_test.go",
		"internal/architecture/assistant_p8h_test.go",
		"internal/architecture/assistant_p8i_test.go",
		"internal/services/inline/diagnostics.go",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("P8-J required acceptance/fence missing %s: %v", rel, err)
		}
	}
}

func TestP8JFinalParityDocumentsAreFrozenClosed(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		"docs/design/assistant-parity-p8a-inventory.md": {
			"P8-B  production self-inline / RenderBridge                 CLOSED",
			"P8-H  unload/reload/generation cross-surface acceptance     CLOSED",
			"P8-I  resource/idle/high-load acceptance                    CLOSED",
			"P8-J  final cleanup, parity freeze, and closure              CLOSED",
		},
		"docs/design/assistant-parity-p8f-behavioral-matrix.md": {
			"## P8 A→J behavioral matrix — P8-J reconciled",
			"| Cross-surface disable/re-enable matrix | old generation fully invalidated | P8-H generation-scoped command/inline/a2/deep-link/self-inline/input/TaskEngine acceptance | MUST / P8-H | CLOSED |",
			"| Combined idle/high-load/resource matrix | bounded under mixed workloads | P8-I 10k inline + callback/session pressure + RPC/cache/resource/restart/settle acceptance | MUST / P8-I | CLOSED |",
			"P8-J CLOSED",
		},
		"docs/design/assistant-parity-p8i-resource-acceptance.md": {
			"## P8 final status — P8-J reconciliation",
			"P8-J CLOSED",
		},
		"docs/design/assistant-parity-p8j-closure.md": {
			"**P8 is CLOSED.**",
			"core.Router",
			"interaction.Runtime",
			"telegram.RPCExecutor",
			"selfinline.Renderer",
			"No P8 capability remains deferred",
		},
	}

	for rel, required := range checks {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		source := string(raw)
		for _, invariant := range required {
			if !strings.Contains(source, invariant) {
				t.Errorf("P8-J closure invariant missing from %s: %q", rel, invariant)
			}
		}
	}
}
