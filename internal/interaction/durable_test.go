package interaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type durableCatalog struct {
	version    string
	generation uint64
}

func (c durableCatalog) FeatureScope(string) (tasks.ScopeIdentity, bool) {
	generation := c.generation
	if generation == 0 {
		generation = 2
	}
	return tasks.ScopeIdentity{Owner: "plugin:demo", Generation: generation}, true
}
func (c durableCatalog) HasAction(_, action string) bool { return action == "next" }
func (c durableCatalog) DurabilityVersion(string) string { return c.version }

type changingDurableCatalog struct{ version *string }

func (c changingDurableCatalog) FeatureScope(string) (tasks.ScopeIdentity, bool) {
	return tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 2}, true
}
func (c changingDurableCatalog) HasAction(_, action string) bool { return action == "next" }
func (c changingDurableCatalog) DurabilityVersion(string) string { return *c.version }

type memoryDurableStore struct {
	rows        map[string]DurableSession
	saveErr     error
	deleteErr   error
	deleteCtx   context.Context
	deleteCalls int
}

func (s *memoryDurableStore) Save(_ context.Context, row DurableSession) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	if s.rows == nil {
		s.rows = make(map[string]DurableSession)
	}
	s.rows[row.Session.ID] = row
	return nil
}

type cancelAwareDurableStore struct {
	*memoryDurableStore
	started chan struct{}
}

func (s *cancelAwareDurableStore) Delete(ctx context.Context, _ string) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestDurableWriteFailureDoesNotAdvanceState(t *testing.T) {
	store := &memoryDurableStore{}
	r, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = r.SetDurableStore(store)
	created, err := r.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("disk unavailable")
	if _, err := r.UpdateState(context.Background(), created.Session.ID, UpdateRequest{ExpectedRevision: 1, State: []byte("new")}); err == nil {
		t.Fatal("update succeeded despite persistence failure")
	}
	got, err := r.Resolve(context.Background(), created.Session.ID, Binding{ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got.Session.Revision != 1 || len(got.Session.State) != 0 {
		t.Fatalf("state advanced: %+v", got.Session)
	}
	if r.Stats().PersistenceErrors != 1 {
		t.Fatalf("persistence errors = %d", r.Stats().PersistenceErrors)
	}
}

func TestDurableInputWriteFailureDoesNotReserveClaim(t *testing.T) {
	store := &memoryDurableStore{}
	r, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = r.SetDurableStore(store)
	created, err := r.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7, ChatID: 8, MessageID: 9}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("disk unavailable")
	if _, err := r.ArmInput(context.Background(), created.Session.ID, InputRequest{ExpectedRevision: 1, TTL: time.Minute}); err == nil {
		t.Fatal("arm succeeded despite persistence failure")
	}
	_, claimed, err := r.TakeInput(context.Background(), 7, 8)
	if err != nil || claimed {
		t.Fatalf("input claimed=%v err=%v", claimed, err)
	}
}
func (s *memoryDurableStore) Delete(ctx context.Context, id string) error {
	s.deleteCtx = ctx
	s.deleteCalls++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.rows, id)
	return nil
}
func (s *memoryDurableStore) Load(context.Context) ([]DurableSession, error) {
	var rows []DurableSession
	for _, row := range s.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func TestDurableSessionSurvivesRuntimeRestart(t *testing.T) {
	store := &memoryDurableStore{}
	first, err := NewRuntime(durableCatalog{version: "1", generation: 1}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	created, err := first.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, State: []byte("state"), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	second, err := NewRuntime(durableCatalog{version: "1"}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	if err := second.RestoreDurable(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := second.Resolve(context.Background(), created.Session.ID, Binding{ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Session.State) != "state" || got.Session.Scope.Generation != 2 {
		t.Fatalf("restored session = %+v", got.Session)
	}
	data, err := EncodeCallbackToken("demo", "next", created.Session.ID, created.Session.Revision)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := second.ResolveCallback(context.Background(), data, Binding{ActorID: 7})
	if err != nil || callback.Token.ActionID != "next" {
		t.Fatalf("callback = %+v, err=%v", callback, err)
	}
}

func TestDurableSessionRejectsChangedVersion(t *testing.T) {
	store := &memoryDurableStore{}
	first, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = first.SetDurableStore(store)
	created, err := first.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewRuntime(durableCatalog{version: "2"}, Config{})
	_ = second.SetDurableStore(store)
	if err := second.RestoreDurable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Resolve(context.Background(), created.Session.ID, Binding{ActorID: 7}); err != ErrNotFound {
		t.Fatalf("resolve error = %v", err)
	}
	if len(store.rows) != 0 {
		t.Fatal("incompatible session retained")
	}
}

func TestDurableSessionUpdateSurvivesRestart(t *testing.T) {
	store := &memoryDurableStore{}
	first, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = first.SetDurableStore(store)
	created, err := first.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.UpdateState(context.Background(), created.Session.ID, UpdateRequest{ExpectedRevision: 1, State: []byte("changed")})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = second.SetDurableStore(store)
	if err := second.RestoreDurable(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := second.Resolve(context.Background(), created.Session.ID, Binding{ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Session.State) != "changed" || got.Session.Revision != 2 {
		t.Fatalf("restored = %+v", got.Session)
	}
}

func TestDurableSessionCancelRemovesStoredRow(t *testing.T) {
	store := &memoryDurableStore{}
	r, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = r.SetDurableStore(store)
	created, err := r.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Cancel(created.Session.ID) {
		t.Fatal("cancel failed")
	}
	if len(store.rows) != 0 {
		t.Fatal("canceled session retained")
	}
}

func TestDurableSessionCancelAfterFeatureUnregisterRemovesStoredRow(t *testing.T) {
	store := &memoryDurableStore{}
	version := "1"
	r, _ := NewRuntime(changingDurableCatalog{version: &version}, Config{})
	_ = r.SetDurableStore(store)
	created, err := r.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	version = ""
	if !r.Cancel(created.Session.ID) {
		t.Fatal("cancel failed")
	}
	if len(store.rows) != 0 {
		t.Fatal("unregistered feature retained stored session")
	}
}

func TestDurableCancelScopeDeleteFailureIsObservable(t *testing.T) {
	store := &memoryDurableStore{}
	r, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = r.SetDurableStore(store)
	created, err := r.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	deleteErr := errors.New("delete unavailable")
	store.deleteErr = deleteErr
	removed, err := r.CancelScopeContext(context.Background(), created.Session.Scope)
	if !errors.Is(err, deleteErr) {
		t.Fatalf("CancelScopeContext() error = %v, want %v", err, deleteErr)
	}
	if removed != 0 {
		t.Fatalf("CancelScopeContext() removed = %d, want 0", removed)
	}
	stats := r.SnapshotStats()
	if stats.Sessions != 1 || stats.PersistenceErrors != 1 {
		t.Fatalf("stats after failed durable delete = %+v", stats)
	}
	if len(store.rows) != 1 {
		t.Fatalf("durable rows after failed delete = %d, want 1", len(store.rows))
	}
}

func TestDurableScopeDeleteHonorsContextCancellation(t *testing.T) {
	store := &cancelAwareDurableStore{memoryDurableStore: &memoryDurableStore{}, started: make(chan struct{})}
	r, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = r.SetDurableStore(store)
	created, err := r.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.CancelScopeContext(ctx, created.Session.Scope)
		done <- err
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("durable delete did not receive lifecycle context")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CancelScopeContext() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("durable delete ignored lifecycle cancellation")
	}
	if stats := r.SnapshotStats(); stats.Sessions != 1 || stats.PersistenceErrors != 1 {
		t.Fatalf("stats after canceled durable delete = %+v", stats)
	}
}

func TestDurableScopeDeleteUsesLifecycleContext(t *testing.T) {
	type contextKey struct{}
	store := &memoryDurableStore{}
	r, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = r.SetDurableStore(store)
	created, err := r.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), contextKey{}, "d1")
	removed, err := r.CancelScopeContext(ctx, created.Session.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("CancelScopeContext() removed = %d, want 1", removed)
	}
	if store.deleteCtx == nil || store.deleteCtx.Value(contextKey{}) != "d1" {
		t.Fatalf("durable delete context = %v, want lifecycle value", store.deleteCtx)
	}
}

func TestDurableShutdownPreservesStoredRow(t *testing.T) {
	store := &memoryDurableStore{}
	r, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = r.SetDurableStore(store)
	created, err := r.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	r.PreserveDurableOnShutdown()
	if !r.Cancel(created.Session.ID) {
		t.Fatal("shutdown cleanup failed")
	}
	if len(store.rows) != 1 {
		t.Fatal("shutdown deleted durable session")
	}
}

func TestDurableInputClaimSurvivesRestart(t *testing.T) {
	store := &memoryDurableStore{}
	first, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = first.SetDurableStore(store)
	created, err := first.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: 7, ChatID: 8, MessageID: 9}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.ArmInput(context.Background(), created.Session.ID, InputRequest{ExpectedRevision: 1, State: []byte("awaiting"), TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = second.SetDurableStore(store)
	if err := second.RestoreDurable(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, claimed, err := second.TakeInput(context.Background(), 7, 8)
	if err != nil || !claimed || got.Session.Revision != 3 {
		t.Fatalf("claim=%v session=%+v err=%v", claimed, got.Session, err)
	}
	third, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = third.SetDurableStore(store)
	if err := third.RestoreDurable(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, claimed, err = third.TakeInput(context.Background(), 7, 8)
	if err != nil || claimed {
		t.Fatalf("duplicate claim=%v err=%v", claimed, err)
	}
}

func TestDurableRestoreRejectsExcessSessions(t *testing.T) {
	store := &memoryDurableStore{}
	first, _ := NewRuntime(durableCatalog{version: "1"}, Config{})
	_ = first.SetDurableStore(store)
	for i := int64(1); i <= 2; i++ {
		if _, err := first.Create(context.Background(), CreateRequest{FeatureID: "demo", Binding: Binding{ActorID: i}, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	second, _ := NewRuntime(durableCatalog{version: "1"}, Config{MaxSessions: 1})
	_ = second.SetDurableStore(store)
	if err := second.RestoreDurable(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := second.Stats()
	if stats.Sessions != 1 || stats.RestoreRejected != 1 {
		t.Fatalf("restore stats = %+v", stats)
	}
}
