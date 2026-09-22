package savedresponse

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

type testResolver struct {
	response Response
	found    bool
}

func (r *testResolver) ResolveSavedResponse(context.Context, Reference) (Response, bool, error) {
	return r.response, r.found, nil
}

func TestRegistryLifecycleAndDefensiveClone(t *testing.T) {
	registry := NewRegistry()
	scope1 := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 1}
	first := &testResolver{response: NewText("first"), found: true}
	reg1, err := registry.Register("notes", scope1, first)
	if err != nil {
		t.Fatalf("Register(first) error = %v", err)
	}

	ref := Reference{Provider: " NOTES ", ScopeID: 42, Key: " hello "}
	resolved, err := registry.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve(first) error = %v", err)
	}
	if resolved.Scope != scope1 || resolved.Reference.Provider != "notes" || resolved.Reference.Key != "hello" {
		t.Fatalf("unexpected resolved metadata: %+v", resolved)
	}
	resolved.Response.Text = "mutated"
	again, err := registry.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve(first again) error = %v", err)
	}
	if again.Response.Text != "first" {
		t.Fatalf("resolved response was not defensively cloned: %q", again.Response.Text)
	}

	reg1.Close()
	if _, err := registry.Resolve(context.Background(), ref); !errors.Is(err, ErrResolverUnavailable) {
		t.Fatalf("Resolve(after close) error = %v, want %v", err, ErrResolverUnavailable)
	}

	scope2 := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 2}
	second := &testResolver{response: NewText("second"), found: true}
	reg2, err := registry.Register("notes", scope2, second)
	if err != nil {
		t.Fatalf("Register(second) error = %v", err)
	}
	defer reg2.Close()

	// A stale cleanup from generation 1 must not remove generation 2.
	reg1.Close()
	resolved, err = registry.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve(second) error = %v", err)
	}
	if resolved.Scope != scope2 || resolved.Response.Text != "second" {
		t.Fatalf("stale cleanup affected replacement: %+v", resolved)
	}
}

func TestRegistryReferenceValidationAndNotFound(t *testing.T) {
	registry := NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 1}
	reg, err := registry.Register("notes", scope, &testResolver{found: false})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	defer reg.Close()

	if _, err := registry.Resolve(context.Background(), Reference{Provider: "notes"}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("invalid reference error = %v, want %v", err, ErrInvalidReference)
	}
	if _, err := registry.Resolve(context.Background(), Reference{Provider: "notes", ScopeID: 1, Key: "missing"}); !errors.Is(err, ErrReferencedNotFound) {
		t.Fatalf("missing reference error = %v, want %v", err, ErrReferencedNotFound)
	}
}
