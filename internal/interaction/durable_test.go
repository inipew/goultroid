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
	rows    map[string]DurableSession
	saveErr error
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
func (s *memoryDurableStore) Delete(_ context.Context, id string) error {
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
