package plugin

import (
	"errors"
	"reflect"
	"testing"
)

func TestResolveOrder_Simple(t *testing.T) {
	manifests := []Manifest{
		{ID: "c", Name: "C", Version: "1.0", Dependencies: []string{"a", "b"}},
		{ID: "b", Name: "B", Version: "1.0", Dependencies: []string{"a"}},
		{ID: "a", Name: "A", Version: "1.0"},
	}

	order, err := ResolveOrder(manifests)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"a", "b", "c"}
	if !reflect.DeepEqual(order, expected) {
		t.Fatalf("expected order %v, got %v", expected, order)
	}
}

func TestResolveOrder_MissingDependency(t *testing.T) {
	manifests := []Manifest{
		{ID: "b", Name: "B", Version: "1.0", Dependencies: []string{"missing"}},
	}

	_, err := ResolveOrder(manifests)
	if !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("expected ErrMissingDependency, got %v", err)
	}
}

func TestResolveOrder_Conflict(t *testing.T) {
	manifests := []Manifest{
		{ID: "plugin-a", Name: "A", Version: "1.0", Conflicts: []string{"plugin-b"}},
		{ID: "plugin-b", Name: "B", Version: "1.0"},
	}

	_, err := ResolveOrder(manifests)
	if !errors.Is(err, ErrPluginConflict) {
		t.Fatalf("expected ErrPluginConflict, got %v", err)
	}
}

func TestResolveOrder_Cycle(t *testing.T) {
	manifests := []Manifest{
		{ID: "a", Name: "A", Version: "1.0", Dependencies: []string{"b"}},
		{ID: "b", Name: "B", Version: "1.0", Dependencies: []string{"a"}},
	}

	_, err := ResolveOrder(manifests)
	if !errors.Is(err, ErrCyclicDependency) {
		t.Fatalf("expected ErrCyclicDependency, got %v", err)
	}
}
