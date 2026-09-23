package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8GAssistantCompatibilityShimsStayReclaimed(t *testing.T) {
	root := repositoryRoot(t)

	appRaw, err := os.ReadFile(filepath.Join(root, "internal", "assistant", "app.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(appRaw), "NewBotClient") {
		t.Fatal("retired NewBotClient compatibility constructor re-entered production")
	}

	routerRaw, err := os.ReadFile(filepath.Join(root, "internal", "assistant", "command", "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	routerSource := string(routerRaw)
	for _, forbidden := range []string{
		"legacyChatForPeer",
		"func (r *Router) Dispatch(",
		"func (r *Router) DispatchMessage(",
	} {
		if strings.Contains(routerSource, forbidden) {
			t.Fatalf("retired Assistant command compatibility shim %q re-entered production", forbidden)
		}
	}
	for _, required := range []string{
		"func (r *Router) DispatchMessageContext(",
		"Assistant message context requires chat identity",
	} {
		if !strings.Contains(routerSource, required) {
			t.Fatalf("canonical Assistant command ingress invariant missing: %q", required)
		}
	}

	updatesRaw, err := os.ReadFile(filepath.Join(root, "internal", "assistant", "client", "updates.go"))
	if err != nil {
		t.Fatal(err)
	}
	updatesSource := string(updatesRaw)
	for _, required := range []string{
		"deps.CmdRouter.DispatchMessageContext(",
		"assistantCommandMessageContext(",
	} {
		if !strings.Contains(updatesSource, required) {
			t.Fatalf("production Assistant command ingress invariant missing: %q", required)
		}
	}

	if _, err := os.Stat(filepath.Join(root, "internal", "assistant", "legacy_stack_test.go")); err != nil {
		t.Fatalf("legacy a1 regression fence must remain present: %v", err)
	}
}
