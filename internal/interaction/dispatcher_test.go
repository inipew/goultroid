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

func TestPreparedActionRejectsReplacedRegistration(t *testing.T) {
	runtime, _, scope := testRuntime(t, Config{})
	dispatcher := NewDispatcher(runtime)
	oldCalled := false
	oldRegistration, err := dispatcher.Register(scope, "demo", "next", func(context.Context, Action) error {
		oldCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("Register(old) error = %v", err)
	}

	created, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 11}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData() error = %v", err)
	}
	prepared, err := dispatcher.Prepare(context.Background(), data, Binding{ActorID: 11})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	oldRegistration.Close()
	newCalled := false
	newRegistration, err := dispatcher.Register(scope, "demo", "next", func(context.Context, Action) error {
		newCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("Register(new) error = %v", err)
	}
	defer newRegistration.Close()

	if err := prepared.Dispatch(context.Background()); !errors.Is(err, ErrHandlerUnavailable) {
		t.Fatalf("prepared Dispatch() error = %v, want %v", err, ErrHandlerUnavailable)
	}
	if oldCalled || newCalled {
		t.Fatalf("stale prepared action executed handler old=%v new=%v", oldCalled, newCalled)
	}
	if err := dispatcher.Dispatch(context.Background(), data, Binding{ActorID: 11}); err != nil {
		t.Fatalf("fresh Dispatch() error = %v", err)
	}
	if !newCalled {
		t.Fatal("fresh dispatch did not reach replacement handler")
	}
}

func TestPreparedActionRejectsRevisionChangedWhileQueued(t *testing.T) {
	runtime, _, scope := testRuntime(t, Config{})
	dispatcher := NewDispatcher(runtime)
	called := false
	registration, err := dispatcher.Register(scope, "demo", "next", func(context.Context, Action) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	defer registration.Close()

	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 12},
		State:     []byte("one"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatalf("CallbackData() error = %v", err)
	}
	prepared, err := dispatcher.Prepare(context.Background(), data, Binding{ActorID: 12})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, err := runtime.UpdateState(context.Background(), created.Session.ID, UpdateRequest{
		ExpectedRevision: created.Session.Revision,
		State:            []byte("two"),
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	if err := prepared.Dispatch(context.Background()); !errors.Is(err, ErrStaleToken) {
		t.Fatalf("prepared Dispatch() error = %v, want %v", err, ErrStaleToken)
	}
	if called {
		t.Fatal("stale revision executed handler")
	}
}

func TestPreparedActionCarriesDynamicExecutionAdmission(t *testing.T) {
	runtime, _, featureScope := testRuntime(t, Config{})
	dispatcher := NewDispatcher(runtime)
	providerScope := tasks.ScopeIdentity{Owner: "plugin:provider", Generation: 9}
	handlerCalled := false

	registration, err := dispatcher.RegisterPrepared(
		featureScope,
		"demo",
		"next",
		func(_ context.Context, action Action) (ActionAdmission, error) {
			if string(action.Session.State) != "lease" {
				t.Fatalf("preparer state=%q, want lease", action.Session.State)
			}
			return ActionAdmission{
				Scope: providerScope,
				Profile: tasks.ExecutionProfile{
					Resources: []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
				},
				State: "prepared-provider",
			}, nil
		},
		func(_ context.Context, action Action) error {
			handlerCalled = true
			if got, _ := action.Preparation.(string); got != "prepared-provider" {
				t.Fatalf("handler preparation=%v", action.Preparation)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("RegisterPrepared() error=%v", err)
	}
	defer registration.Close()

	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 21},
		State:     []byte("lease"),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dispatcher.Prepare(context.Background(), data, Binding{ActorID: 21})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Scope() != providerScope {
		t.Fatalf("prepared scope=%+v, want %+v", prepared.Scope(), providerScope)
	}
	aware, ok := prepared.(ResourcePreparedAction)
	if !ok {
		t.Fatal("prepared action did not expose resource admission")
	}
	resources := aware.Resources()
	if len(resources) != 1 || resources[0].Name != "media" || resources[0].Amount != 1 {
		t.Fatalf("prepared resources=%+v, want media:1", resources)
	}
	if err := prepared.Dispatch(context.Background()); err != nil {
		t.Fatalf("Dispatch() error=%v", err)
	}
	if !handlerCalled {
		t.Fatal("prepared handler was not called")
	}
}

func TestPreparedActionPreparerFailureStopsBeforeHandler(t *testing.T) {
	runtime, _, scope := testRuntime(t, Config{})
	dispatcher := NewDispatcher(runtime)
	want := errors.New("provider unavailable")
	handlerCalled := false
	registration, err := dispatcher.RegisterPrepared(
		scope,
		"demo",
		"next",
		func(context.Context, Action) (ActionAdmission, error) {
			return ActionAdmission{}, want
		},
		func(context.Context, Action) error {
			handlerCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()

	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo", Binding: Binding{ActorID: 22},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := runtime.CallbackData(context.Background(), created.Session.ID, "next")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Prepare(context.Background(), data, Binding{ActorID: 22}); !errors.Is(err, want) {
		t.Fatalf("Prepare() error=%v, want %v", err, want)
	}
	if handlerCalled {
		t.Fatal("handler ran despite prepare failure")
	}
}
