package interaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestCloseCancelsSessionsAndRejectsNewWork(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 1}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !errors.Is(context.Cause(created.Context), ErrClosed) {
		t.Fatalf("context cause = %v, want %v", context.Cause(created.Context), ErrClosed)
	}
	if _, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 2}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Create() after close error = %v, want %v", err, ErrClosed)
	}
}

func TestCreateDetectsFeatureRemoved(t *testing.T) {
	runtime, catalog, _ := testRuntime(t, Config{})
	catalog.removeScope("demo")
	if _, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 1}}); !errors.Is(err, ErrInvalidFeature) {
		t.Fatalf("Create() error = %v, want invalid feature", err)
	}
}

func TestExpiryHeapRemainsBoundedAcrossTouches(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 1}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for i := 0; i < 1000; i++ {
		if _, err := runtime.Touch(context.Background(), created.Session.ID, time.Hour); err != nil {
			t.Fatalf("Touch(%d) error = %v", i, err)
		}
	}
	runtime.mu.Lock()
	heapEntries := runtime.expiries.Len()
	sessions := len(runtime.sessions)
	runtime.mu.Unlock()
	if heapEntries != sessions || heapEntries != 1 {
		t.Fatalf("expiry heap = %d, sessions = %d, want both 1", heapEntries, sessions)
	}
}

type flappingCatalog struct {
	scope tasks.ScopeIdentity
	calls int
}

func (c *flappingCatalog) FeatureScope(string) (tasks.ScopeIdentity, bool) {
	c.calls++
	if c.calls == 1 {
		return c.scope, true
	}
	return tasks.ScopeIdentity{}, false
}

func (*flappingCatalog) HasAction(string, string) bool { return true }

func TestCreateClosesPublishVsDisableRace(t *testing.T) {
	catalog := &flappingCatalog{scope: tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1}}
	runtime, err := NewRuntime(catalog, Config{})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()
	if _, err := runtime.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 1}}); !errors.Is(err, ErrScopeStale) {
		t.Fatalf("Create() error = %v, want %v", err, ErrScopeStale)
	}
	if stats := runtime.Stats(); stats.Sessions != 0 {
		t.Fatalf("sessions = %d, want 0 after stale publish", stats.Sessions)
	}
}
