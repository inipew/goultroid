package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8HCrossSurfaceLifecycleUsesCanonicalGenerationFences(t *testing.T) {
	root := repositoryRoot(t)

	featuresRaw, err := os.ReadFile(filepath.Join(root, "internal", "plugin", "features.go"))
	if err != nil {
		t.Fatal(err)
	}
	features := string(featuresRaw)
	for _, required := range []string{
		"registry.actions.UnregisterScope(scope)",
		"registry.interactions.CancelScope(scope)",
		"inlineRegistration.Close()",
		"savedResponseRegistration.Close()",
	} {
		if !strings.Contains(features, required) {
			t.Fatalf("feature lifecycle fence missing: %q", required)
		}
	}

	managerRaw, err := os.ReadFile(filepath.Join(root, "internal", "plugin", "manager.go"))
	if err != nil {
		t.Fatal(err)
	}
	manager := string(managerRaw)
	for _, required := range []string{
		"taskClient.CancelScope(tasks.ScopeIdentity{Owner: scope.Owner(), Generation: scope.Generation()}, tasks.CauseScopeClosed)",
		"featureCleanup()",
		"router.UnregisterBatch(cmds)",
	} {
		if !strings.Contains(manager, required) {
			t.Fatalf("plugin disable lifecycle fence missing: %q", required)
		}
	}

	selfInlineRaw, err := os.ReadFile(filepath.Join(root, "internal", "app", "selfinline_features.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(selfInlineRaw), "!manager.IsEnabled(pluginID)") {
		t.Fatal("self-inline authorization is not fenced by current plugin enable state")
	}
}
