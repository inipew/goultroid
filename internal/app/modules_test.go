package app

import (
	"reflect"
	"sort"
	"testing"

	"github.com/inipew/goultroid/internal/module"
)

func TestBuiltinModulesHaveUniqueDeterministicIDs(t *testing.T) {
	if len(builtinModules) == 0 {
		t.Fatal("builtin module registry is empty")
	}

	ids := make([]string, 0, len(builtinModules))
	seen := make(map[string]struct{}, len(builtinModules))
	for _, m := range builtinModules {
		if err := module.Validate(m); err != nil {
			t.Fatalf("invalid builtin module: %v", err)
		}
		id := m.Manifest().ID
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate builtin module ID %q", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(ids, sorted) {
		t.Fatalf("generated builtin module order is not deterministic by ID: got %v, want %v", ids, sorted)
	}
}

func TestBuiltinModulesResolveInDependencyOrder(t *testing.T) {
	ordered, err := module.ResolveOrder(builtinModules)
	if err != nil {
		t.Fatalf("resolve builtin module dependencies: %v", err)
	}
	if len(ordered) != len(builtinModules) {
		t.Fatalf("resolved %d modules, registered %d", len(ordered), len(builtinModules))
	}

	position := make(map[string]int, len(ordered))
	for i, m := range ordered {
		position[m.Manifest().ID] = i
	}
	for _, m := range ordered {
		for _, dep := range m.Manifest().Dependencies {
			if position[dep] >= position[m.Manifest().ID] {
				t.Fatalf("module %q appears before dependency %q", m.Manifest().ID, dep)
			}
		}
	}
}
