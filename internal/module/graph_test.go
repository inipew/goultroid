package module

import (
	"context"
	"strings"
	"testing"
)

type dummyModule struct {
	manifest Manifest
}

func (d dummyModule) Manifest() Manifest                       { return d.manifest }
func (d dummyModule) Register(context.Context, *Runtime) error { return nil }

func TestResolveOrder_Empty(t *testing.T) {
	res, err := ResolveOrder(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("expected empty, got %d", len(res))
	}
}

func TestResolveOrder_ValidLinear(t *testing.T) {
	// a depends on b, b depends on c
	mA := dummyModule{Manifest{ID: "a", Version: "1.0.0", Dependencies: []string{"b"}}}
	mB := dummyModule{Manifest{ID: "b", Version: "1.0.0", Dependencies: []string{"c"}}}
	mC := dummyModule{Manifest{ID: "c", Version: "1.0.0"}}

	res, err := ResolveOrder([]Module{mA, mB, mC})
	if err != nil {
		t.Fatalf("resolve order failed: %v", err)
	}

	expected := []string{"c", "b", "a"}
	for i, m := range res {
		if m.Manifest().ID != expected[i] {
			t.Fatalf("index %d expected %s, got %s", i, expected[i], m.Manifest().ID)
		}
	}
}

func TestResolveOrder_DiamondDependencies(t *testing.T) {
	// a depends on b and c; b and c depend on d
	mA := dummyModule{Manifest{ID: "a", Version: "1.0.0", Dependencies: []string{"b", "c"}}}
	mB := dummyModule{Manifest{ID: "b", Version: "1.0.0", Dependencies: []string{"d"}}}
	mC := dummyModule{Manifest{ID: "c", Version: "1.0.0", Dependencies: []string{"d"}}}
	mD := dummyModule{Manifest{ID: "d", Version: "1.0.0"}}

	res, err := ResolveOrder([]Module{mA, mB, mC, mD})
	if err != nil {
		t.Fatalf("resolve order failed: %v", err)
	}

	pos := make(map[string]int)
	for i, m := range res {
		pos[m.Manifest().ID] = i
	}

	if !(pos["d"] < pos["b"] && pos["d"] < pos["c"] && pos["b"] < pos["a"] && pos["c"] < pos["a"]) {
		t.Fatalf("invalid topological order: %v", pos)
	}
}

func TestResolveOrder_CycleDetection(t *testing.T) {
	// a -> b -> c -> a
	mA := dummyModule{Manifest{ID: "a", Version: "1.0.0", Dependencies: []string{"b"}}}
	mB := dummyModule{Manifest{ID: "b", Version: "1.0.0", Dependencies: []string{"c"}}}
	mC := dummyModule{Manifest{ID: "c", Version: "1.0.0", Dependencies: []string{"a"}}}

	_, err := ResolveOrder([]Module{mA, mB, mC})
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "dependency cycle detected") {
		t.Fatalf("expected cycle message, got: %v", err)
	}
}

func TestResolveOrder_SelfDependency(t *testing.T) {
	mA := dummyModule{Manifest{ID: "self", Version: "1.0.0", Dependencies: []string{"self"}}}
	_, err := ResolveOrder([]Module{mA})
	if err == nil {
		t.Fatal("expected self dependency error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot depend on itself") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveOrder_MissingDependency(t *testing.T) {
	mA := dummyModule{Manifest{ID: "mod-a", Version: "1.0.0", Dependencies: []string{"mod-missing"}}}
	_, err := ResolveOrder([]Module{mA})
	if err == nil {
		t.Fatal("expected missing dependency error, got nil")
	}
	if !strings.Contains(err.Error(), "depends on unknown module") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveOrder_DuplicateID(t *testing.T) {
	m1 := dummyModule{Manifest{ID: "dup", Version: "1.0.0"}}
	m2 := dummyModule{Manifest{ID: "dup", Version: "1.0.0"}}
	_, err := ResolveOrder([]Module{m1, m2})
	if err == nil {
		t.Fatal("expected duplicate ID error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate module ID") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveOrder_InvalidID(t *testing.T) {
	m := dummyModule{Manifest{ID: "Invalid ID with spaces", Version: "1.0.0"}}
	_, err := ResolveOrder([]Module{m})
	if err == nil {
		t.Fatal("expected invalid ID error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid module ID") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveOrder_DeterministicAlphabetical(t *testing.T) {
	mZ := dummyModule{Manifest{ID: "zebra", Version: "1.0.0"}}
	mA := dummyModule{Manifest{ID: "apple", Version: "1.0.0"}}
	mM := dummyModule{Manifest{ID: "mango", Version: "1.0.0"}}

	res, err := ResolveOrder([]Module{mZ, mA, mM})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"apple", "mango", "zebra"}
	for i, m := range res {
		if m.Manifest().ID != expected[i] {
			t.Fatalf("index %d expected %s, got %s", i, expected[i], m.Manifest().ID)
		}
	}
}

func TestResolveOrder_RejectsActiveConflict(t *testing.T) {
	mA := dummyModule{Manifest{ID: "a", Version: "1.0.0", Conflicts: []string{"b"}}}
	mB := dummyModule{Manifest{ID: "b", Version: "1.0.0"}}

	_, err := ResolveOrder([]Module{mB, mA})
	if err == nil || !strings.Contains(err.Error(), `module "a" conflicts with module "b"`) {
		t.Fatalf("expected active conflict error, got %v", err)
	}
}

func TestResolveOrder_AllowsAbsentConflict(t *testing.T) {
	mA := dummyModule{Manifest{ID: "a", Version: "1.0.0", Conflicts: []string{"b"}}}

	if _, err := ResolveOrder([]Module{mA}); err != nil {
		t.Fatalf("absent conflict should be allowed: %v", err)
	}
}

func TestResolveOrder_RejectsInvalidConflictDeclarations(t *testing.T) {
	tests := []struct {
		name      string
		conflicts []string
		want      string
	}{
		{name: "self", conflicts: []string{"a"}, want: "cannot conflict with itself"},
		{name: "duplicate", conflicts: []string{"b", " b "}, want: "duplicate conflict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			module := dummyModule{Manifest{ID: "a", Version: "1.0.0", Conflicts: tt.conflicts}}
			_, err := ResolveOrder([]Module{module})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}
