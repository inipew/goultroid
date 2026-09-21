package interaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestInputClaimIsBoundedOneShotAndConsumesRevision(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7, ChatID: 9, MessageID: 11},
		State:     []byte("detail"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	armed, err := runtime.ArmInput(context.Background(), created.Session.ID, InputRequest{
		ExpectedRevision: created.Session.Revision,
		State:            []byte("input"),
		TTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("ArmInput() error = %v", err)
	}
	if armed.Revision != created.Session.Revision+1 {
		t.Fatalf("armed revision = %d, want %d", armed.Revision, created.Session.Revision+1)
	}
	if stats := runtime.Stats(); stats.Inputs != 1 || stats.Sessions != 1 {
		t.Fatalf("stats after arm = %+v", stats)
	}

	resolved, handled, err := runtime.TakeInput(context.Background(), 7, 9)
	if err != nil {
		t.Fatalf("TakeInput() error = %v", err)
	}
	if !handled || resolved.Session.Revision != armed.Revision+1 || string(resolved.Session.State) != "input" {
		t.Fatalf("take result handled=%v session=%+v", handled, resolved.Session)
	}
	if stats := runtime.Stats(); stats.Inputs != 0 || stats.Sessions != 1 {
		t.Fatalf("stats after take = %+v", stats)
	}
	if _, handled, err := runtime.TakeInput(context.Background(), 7, 9); err != nil || handled {
		t.Fatalf("second TakeInput() handled=%v err=%v", handled, err)
	}
}

func TestInputClaimFailsClosedForSameActorChat(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{})
	first, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7, ChatID: 9, MessageID: 11},
	})
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	second, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7, ChatID: 9, MessageID: 12},
	})
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if _, err := runtime.ArmInput(context.Background(), first.Session.ID, InputRequest{
		ExpectedRevision: first.Session.Revision,
		State:            []byte("first"),
		TTL:              time.Minute,
	}); err != nil {
		t.Fatalf("first ArmInput() error = %v", err)
	}
	if _, err := runtime.ArmInput(context.Background(), second.Session.ID, InputRequest{
		ExpectedRevision: second.Session.Revision,
		State:            []byte("second"),
		TTL:              time.Minute,
	}); !errors.Is(err, ErrInputBusy) {
		t.Fatalf("second ArmInput() error = %v, want %v", err, ErrInputBusy)
	}
}

func TestInputClaimExpiresLazilyWithoutSessionGoroutine(t *testing.T) {
	runtime, _, _ := testRuntime(t, Config{DefaultTTL: time.Hour, MaxTTL: time.Hour})
	now := time.Unix(100, 0)
	runtime.now = func() time.Time { return now }
	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7, ChatID: 9, MessageID: 11},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := runtime.ArmInput(context.Background(), created.Session.ID, InputRequest{
		ExpectedRevision: created.Session.Revision,
		State:            []byte("input"),
		TTL:              time.Minute,
	}); err != nil {
		t.Fatalf("ArmInput() error = %v", err)
	}
	now = now.Add(time.Minute)
	if _, handled, err := runtime.TakeInput(context.Background(), 7, 9); !handled || !errors.Is(err, ErrInputExpired) {
		t.Fatalf("expired TakeInput() handled=%v err=%v", handled, err)
	}
	if stats := runtime.Stats(); stats.Inputs != 0 || stats.Sessions != 1 {
		t.Fatalf("stats after input expiry = %+v", stats)
	}
}

func TestInputClaimClearsOnStateChangeScopeCancelAndRelease(t *testing.T) {
	runtime, catalog, scope := testRuntime(t, Config{})
	created, err := runtime.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7, ChatID: 9, MessageID: 11},
		State:     []byte("detail"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	armed, err := runtime.ArmInput(context.Background(), created.Session.ID, InputRequest{
		ExpectedRevision: created.Session.Revision,
		State:            []byte("input"),
		TTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("ArmInput() error = %v", err)
	}
	updated, err := runtime.UpdateState(context.Background(), created.Session.ID, UpdateRequest{
		ExpectedRevision: armed.Revision,
		State:            []byte("home"),
	})
	if err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	if stats := runtime.Stats(); stats.Inputs != 0 {
		t.Fatalf("input survived state transition: %+v", stats)
	}

	armed, err = runtime.ArmInput(context.Background(), created.Session.ID, InputRequest{
		ExpectedRevision: updated.Revision,
		State:            []byte("input-again"),
		TTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("second ArmInput() error = %v", err)
	}
	if !runtime.ReleaseInput(created.Session.ID) || runtime.Stats().Inputs != 0 {
		t.Fatal("ReleaseInput() did not clear claim")
	}

	armed, err = runtime.ArmInput(context.Background(), created.Session.ID, InputRequest{
		ExpectedRevision: armed.Revision,
		State:            []byte("input-third"),
		TTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("third ArmInput() error = %v", err)
	}
	catalog.setScope("demo", tasks.ScopeIdentity{Owner: scope.Owner, Generation: scope.Generation + 1})
	if got := runtime.CancelScope(scope); got != 1 {
		t.Fatalf("CancelScope() = %d, want 1", got)
	}
	if stats := runtime.Stats(); stats.Inputs != 0 || stats.Sessions != 0 {
		t.Fatalf("claim survived generation cleanup: %+v", stats)
	}
}
