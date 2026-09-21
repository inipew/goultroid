package interaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestCreateResolveBindingAndDefensiveState(t *testing.T) {
	runtime, _, scope := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 10, ChatID: 20},
		State:     []byte("state"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Session.Scope != scope {
		t.Fatalf("scope = %+v, want %+v", created.Session.Scope, scope)
	}
	created.Session.State[0] = 'X'

	resolved, err := runtime.Resolve(context.Background(), created.Session.ID, Binding{ActorID: 10, ChatID: 20})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if string(resolved.Session.State) != "state" {
		t.Fatalf("stored state = %q, want state", resolved.Session.State)
	}
	if _, err := runtime.Resolve(context.Background(), created.Session.ID, Binding{ActorID: 11, ChatID: 20}); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("Resolve() mismatch error = %v, want %v", err, ErrBindingMismatch)
	}
}

func TestTTLExpiryCancelsSessionContext(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{DefaultTTL: time.Minute, MaxTTL: time.Hour})
	now := time.Unix(100, 0)
	runtime.now = func() time.Time { return now }
	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 10},
		State:     []byte("x"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := runtime.Resolve(context.Background(), created.Session.ID, Binding{ActorID: 10}); !errors.Is(err, ErrExpired) {
		t.Fatalf("Resolve() error = %v, want %v", err, ErrExpired)
	}
	select {
	case <-created.Context.Done():
		if !errors.Is(context.Cause(created.Context), ErrExpired) {
			t.Fatalf("context cause = %v, want %v", context.Cause(created.Context), ErrExpired)
		}
	default:
		t.Fatal("expired session context remains active")
	}
}

func TestGenerationChangeInvalidatesSession(t *testing.T) {
	runtime, catalog, scope := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 10},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	catalog.setScope("demo", tasks.ScopeIdentity{Owner: scope.Owner, Generation: scope.Generation + 1})
	if _, err := runtime.Resolve(context.Background(), created.Session.ID, Binding{ActorID: 10}); !errors.Is(err, ErrScopeStale) {
		t.Fatalf("Resolve() error = %v, want %v", err, ErrScopeStale)
	}
	if !errors.Is(context.Cause(created.Context), ErrScopeStale) {
		t.Fatalf("context cause = %v, want stale scope", context.Cause(created.Context))
	}
}

func TestCancelScopeOnlyCancelsOwnedGeneration(t *testing.T) {
	runtime, catalog, scope1 := testRuntime(t, Config{})
	first, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 10}})
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	scope2 := tasks.ScopeIdentity{Owner: scope1.Owner, Generation: scope1.Generation + 1}
	catalog.setScope("demo", scope2)
	if got := runtime.CancelScope(scope1); got != 1 {
		t.Fatalf("CancelScope() = %d, want 1", got)
	}
	if !errors.Is(context.Cause(first.Context), ErrScopeStale) {
		t.Fatalf("first context cause = %v", context.Cause(first.Context))
	}
	second, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 11}})
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if second.Session.Scope != scope2 {
		t.Fatalf("second scope = %+v, want %+v", second.Session.Scope, scope2)
	}
}

func TestCapacityFailsClosedWithoutEvictingLiveSession(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{
		MaxSessions:         1,
		MaxSessionsPerScope: 1,
		MaxSessionsPerActor: 1,
		MaxStateBytes:       8,
		MaxTotalStateBytes:  8,
	})
	first, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 1}, State: []byte("1234")})
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if _, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 2}, State: []byte("1")}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("second Create() error = %v, want %v", err, ErrCapacity)
	}
	if _, err := runtime.Resolve(context.Background(), first.Session.ID, Binding{ActorID: 1}); err != nil {
		t.Fatalf("live session was evicted: %v", err)
	}
}
