package interaction

import (
	"context"
	"errors"
	"testing"
)

type dispatcherContextKey struct{}

func TestDispatcherPreservesCallerValuesAndSessionCancellation(t *testing.T) {
	runtime, _, scope := testRuntime(t, Config{})
	dispatcher := NewDispatcher(runtime)
	created, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 1}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData() error = %v", err)
	}
	registration, err := dispatcher.Register(scope, "demo", "next", func(ctx context.Context, _ Action) error {
		if got := ctx.Value(dispatcherContextKey{}); got != "caller" {
			t.Fatalf("caller value = %v", got)
		}
		runtime.Cancel(created.Session.ID)
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), ErrCanceled) {
			t.Fatalf("context cause = %v, want %v", context.Cause(ctx), ErrCanceled)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	defer registration.Close()
	ctx := context.WithValue(context.Background(), dispatcherContextKey{}, "caller")
	if err := dispatcher.Dispatch(ctx, data, Binding{ActorID: 1}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
}

func TestDispatcherUnregisterScopeIsGenerationExact(t *testing.T) {
	runtime, catalog, scope1 := testRuntime(t, Config{})
	dispatcher := NewDispatcher(runtime)
	old, err := dispatcher.Register(scope1, "demo", "next", func(context.Context, Action) error { return nil })
	if err != nil {
		t.Fatalf("Register(old) error = %v", err)
	}
	defer old.Close()

	scope2 := scope1
	scope2.Generation++
	catalog.setScope("demo", scope2)
	newRegistration, err := dispatcher.Register(scope2, "demo", "next", func(context.Context, Action) error { return nil })
	if err != nil {
		t.Fatalf("Register(new) error = %v", err)
	}
	defer newRegistration.Close()

	if removed := dispatcher.UnregisterScope(scope1); removed != 0 {
		t.Fatalf("UnregisterScope(old) removed %d current handlers, want 0", removed)
	}
	if removed := dispatcher.UnregisterScope(scope2); removed != 1 {
		t.Fatalf("UnregisterScope(new) removed %d handlers, want 1", removed)
	}
}
