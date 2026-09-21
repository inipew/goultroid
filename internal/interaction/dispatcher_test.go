package interaction

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestDispatcherValidatesP1BeforeHandler(t *testing.T) {
	runtime, _, scope := testRuntime(t, Config{})
	dispatcher := NewDispatcher(runtime)
	called := 0
	registration, err := dispatcher.Register(scope, "demo", "next", func(ctx context.Context, action Action) error {
		called++
		if action.Session.FeatureID != "demo" || action.Token.ActionID != "next" {
			t.Fatalf("unexpected action: %+v", action)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	defer registration.Close()

	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData() error = %v", err)
	}
	if err := dispatcher.Dispatch(context.Background(), data, Binding{ActorID: 7}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if called != 1 {
		t.Fatalf("handler calls = %d, want 1", called)
	}
	if err := dispatcher.Dispatch(context.Background(), data, Binding{ActorID: 8}); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("Dispatch(wrong actor) error = %v, want %v", err, ErrBindingMismatch)
	}
	if called != 1 {
		t.Fatalf("handler invoked after failed validation")
	}
}

func TestDispatcherStaleCleanupCannotRemoveNewGeneration(t *testing.T) {
	runtime, catalog, scope1 := testRuntime(t, Config{})
	dispatcher := NewDispatcher(runtime)
	old, err := dispatcher.Register(scope1, "demo", "next", func(context.Context, Action) error { return nil })
	if err != nil {
		t.Fatalf("Register(old) error = %v", err)
	}

	scope2 := tasks.ScopeIdentity{Owner: scope1.Owner, Generation: scope1.Generation + 1}
	catalog.setScope("demo", scope2)
	newCalled := false
	newRegistration, err := dispatcher.Register(scope2, "demo", "next", func(context.Context, Action) error {
		newCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("Register(new) error = %v", err)
	}
	defer newRegistration.Close()
	old.Close()

	created, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 9}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData() error = %v", err)
	}
	if err := dispatcher.Dispatch(context.Background(), data, Binding{ActorID: 9}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if !newCalled {
		t.Fatal("new generation handler was removed by stale cleanup")
	}
}
