package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP0DInlineRegistryKeepsExactIndexSeparateFromCustomMatchers(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "services", "inline", "engine.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	for _, required := range []string{
		"customEntries []registryEntry",
		"exact         bool",
		"registryEntryPrecedes(entry, direct)",
		"directOK && direct.pattern != \"\" && direct.exact",
		"for _, entry := range r.customEntries",
		"Preserve the historical direct-pattern fallback",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("P0-D inline exact fast-path invariant missing %q", required)
		}
	}
	if strings.Contains(source, "for _, entry := range r.entries") {
		t.Fatal("ResolveOwnedExplicit regressed to scanning the full exact+custom registry")
	}

	benchPath := filepath.Join(root, "internal", "services", "inline", "registry_fastpath_benchmark_test.go")
	benchRaw, err := os.ReadFile(benchPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, cardinality := range []string{"1, 16, 64, 256", "exact/%d", "custom/%d"} {
		if !strings.Contains(string(benchRaw), cardinality) {
			t.Fatalf("P0-D benchmark matrix missing %q", cardinality)
		}
	}
}
