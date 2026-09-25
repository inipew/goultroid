package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP0AssistantIdentityFailsClosedOutsideCurrentRunningRun(t *testing.T) {
	root := repositoryRoot(t)
	clientPath := filepath.Join(root, "internal", "assistant", "client", "identity.go")
	raw, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"c.lifecycle.State() != StateRunning",
		"return \"\"",
		"case StateFailed:",
		"return ErrNotReady",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Assistant identity/readiness invariant missing %q", required)
		}
	}

	clientRaw, err := os.ReadFile(filepath.Join(root, "internal", "assistant", "client", "client.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(clientRaw), `return "GoUltroidBot"`) {
		t.Fatal("Assistant still exposes fabricated fallback username")
	}
}
