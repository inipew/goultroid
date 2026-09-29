package interaction

import (
	"context"
	"errors"
	"testing"
	"time"
)

type selectiveDeleteDurableStore struct {
	*memoryDurableStore
	failID string
	err    error
}

func (s *selectiveDeleteDurableStore) Delete(ctx context.Context, id string) error {
	s.deleteCtx = ctx
	s.deleteCalls++
	if id == s.failID {
		return s.err
	}
	delete(s.rows, id)
	return nil
}

func TestDurableExpiryDeleteFailureRemainsTracked(t *testing.T) {
	store := &memoryDurableStore{}
	r, err := NewRuntime(durableCatalog{version: "1"}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	r.now = func() time.Time { return now }
	created, err := r.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7},
		State:     []byte("state"),
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	deleteErr := errors.New("delete unavailable")
	store.deleteErr = deleteErr
	now = now.Add(time.Minute)
	if removed := r.PruneExpired(); removed != 0 {
		t.Fatalf("PruneExpired() removed = %d, want 0", removed)
	}
	if store.deleteCalls != 1 {
		t.Fatalf("durable delete calls = %d, want 1", store.deleteCalls)
	}

	r.mu.Lock()
	entry := r.sessions[created.Session.ID]
	heapLen := r.expiries.Len()
	scopeCount := len(r.byScope[created.Session.Scope])
	actorCount := r.actorCounts[7]
	stateBytes := r.stateBytes
	expiryTracked := entry != nil && entry.expiry != nil && entry.expiry.index >= 0
	r.mu.Unlock()
	if !expiryTracked || heapLen != 1 {
		t.Fatalf("failed expiry delete lost retry tracking: tracked=%v heap=%d", expiryTracked, heapLen)
	}
	if scopeCount != 1 || actorCount != 1 || stateBytes != len("state") {
		t.Fatalf("failed expiry delete corrupted accounting: scope=%d actor=%d state=%d", scopeCount, actorCount, stateBytes)
	}
	stats := r.SnapshotStats()
	if stats.Sessions != 1 || stats.Expired != 0 || stats.PersistenceErrors != 1 {
		t.Fatalf("stats after failed expiry delete = %+v", stats)
	}
	if _, err := r.Resolve(context.Background(), created.Session.ID, Binding{ActorID: 7}); !errors.Is(err, ErrExpired) {
		t.Fatalf("Resolve() error = %v, want %v", err, ErrExpired)
	}
	r.mu.Lock()
	if r.expiries.Len() != 1 || r.sessions[created.Session.ID] == nil || r.sessions[created.Session.ID].expiry == nil {
		r.mu.Unlock()
		t.Fatal("expired session stopped being cleanup-tracked after resolve")
	}
	r.mu.Unlock()
}

func TestDurableExpiryDeleteRetryRemovesExactlyOnce(t *testing.T) {
	store := &memoryDurableStore{}
	r, err := NewRuntime(durableCatalog{version: "1"}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0)
	r.now = func() time.Time { return now }
	created, err := r.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7},
		State:     []byte("state"),
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	store.deleteErr = errors.New("delete unavailable")
	now = now.Add(time.Minute)
	if removed := r.PruneExpired(); removed != 0 {
		t.Fatalf("first PruneExpired() removed = %d, want 0", removed)
	}
	if removed := r.PruneExpired(); removed != 0 {
		t.Fatalf("second failed PruneExpired() removed = %d, want 0", removed)
	}
	r.mu.Lock()
	heapLen := r.expiries.Len()
	r.mu.Unlock()
	if heapLen != 1 {
		t.Fatalf("expiry heap after repeated failures = %d, want 1", heapLen)
	}
	if store.deleteCalls != 2 {
		t.Fatalf("delete calls after two prune passes = %d, want 2", store.deleteCalls)
	}

	store.deleteErr = nil
	if removed := r.PruneExpired(); removed != 1 {
		t.Fatalf("recovery PruneExpired() removed = %d, want 1", removed)
	}
	if store.deleteCalls != 3 {
		t.Fatalf("delete calls after recovery = %d, want 3", store.deleteCalls)
	}
	if _, ok := store.rows[created.Session.ID]; ok {
		t.Fatal("durable row remained after successful expiry retry")
	}
	stats := r.SnapshotStats()
	if stats.Sessions != 0 || stats.StateBytes != 0 || stats.Expired != 1 {
		t.Fatalf("stats after successful expiry retry = %+v", stats)
	}
	r.mu.Lock()
	heapLen = r.expiries.Len()
	r.mu.Unlock()
	if heapLen != 0 {
		t.Fatalf("expiry heap after successful retry = %d, want 0", heapLen)
	}
	if removed := r.PruneExpired(); removed != 0 {
		t.Fatalf("post-removal PruneExpired() removed = %d, want 0", removed)
	}
	if store.deleteCalls != 3 {
		t.Fatalf("session deleted more than once, calls = %d", store.deleteCalls)
	}
}

func TestDurableExpiryDeleteFailurePreservesCapacityAccounting(t *testing.T) {
	store := &memoryDurableStore{}
	r, err := NewRuntime(durableCatalog{version: "1"}, Config{
		MaxSessions:         1,
		MaxSessionsPerScope: 1,
		MaxSessionsPerActor: 1,
		MaxStateBytes:       4,
		MaxTotalStateBytes:  4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(300, 0)
	r.now = func() time.Time { return now }
	if _, err := r.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7},
		State:     []byte("1234"),
		TTL:       time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	store.deleteErr = errors.New("delete unavailable")
	now = now.Add(time.Minute)
	if removed := r.PruneExpired(); removed != 0 {
		t.Fatalf("PruneExpired() removed = %d, want 0", removed)
	}
	if _, err := r.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 8},
		State:     []byte("1"),
	}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("Create() with retained failed-expiry session error = %v, want %v", err, ErrCapacity)
	}

	store.deleteErr = nil
	if removed := r.PruneExpired(); removed != 1 {
		t.Fatalf("recovery PruneExpired() removed = %d, want 1", removed)
	}
	if _, err := r.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 8},
		State:     []byte("1"),
	}); err != nil {
		t.Fatalf("Create() after recovered expiry cleanup error = %v", err)
	}
}

func TestDurableExpiryDeleteFailureCleansExpiredInputClaim(t *testing.T) {
	store := &memoryDurableStore{}
	r, err := NewRuntime(durableCatalog{version: "1"}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(400, 0)
	r.now = func() time.Time { return now }
	created, err := r.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7, ChatID: 8, MessageID: 9},
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ArmInput(context.Background(), created.Session.ID, InputRequest{
		ExpectedRevision: 1,
		State:            []byte("wait"),
		TTL:              time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	if stats := r.SnapshotStats(); stats.Inputs != 1 {
		t.Fatalf("inputs before expiry = %d, want 1", stats.Inputs)
	}

	store.deleteErr = errors.New("delete unavailable")
	now = now.Add(time.Minute)
	if removed := r.PruneExpired(); removed != 0 {
		t.Fatalf("PruneExpired() removed = %d, want 0", removed)
	}
	stats := r.SnapshotStats()
	if stats.Sessions != 1 || stats.Inputs != 0 || stats.StateBytes != len("wait") {
		t.Fatalf("stats after failed session delete = %+v", stats)
	}
	r.mu.Lock()
	entry := r.sessions[created.Session.ID]
	heapLen := r.expiries.Len()
	inputCleared := entry != nil && entry.input == nil
	r.mu.Unlock()
	if !inputCleared || heapLen != 1 {
		t.Fatalf("input/expiry tracking after failed delete: inputCleared=%v heap=%d", inputCleared, heapLen)
	}

	store.deleteErr = nil
	if removed := r.PruneExpired(); removed != 1 {
		t.Fatalf("recovery PruneExpired() removed = %d, want 1", removed)
	}
	if stats := r.SnapshotStats(); stats.Sessions != 0 || stats.Inputs != 0 || stats.StateBytes != 0 || stats.Expired != 1 {
		t.Fatalf("stats after recovery = %+v", stats)
	}
}

func TestDurableExpiryDeleteFailureDoesNotBlockOtherExpiredSessions(t *testing.T) {
	base := &memoryDurableStore{}
	store := &selectiveDeleteDurableStore{memoryDurableStore: base, err: errors.New("selected delete unavailable")}
	r, err := NewRuntime(durableCatalog{version: "1"}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetDurableStore(store); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(500, 0)
	r.now = func() time.Time { return now }
	first, err := r.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 7},
		TTL:       time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Create(context.Background(), CreateRequest{
		FeatureID: "demo",
		Binding:   Binding{ActorID: 8},
		TTL:       2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.failID = first.Session.ID
	now = now.Add(2 * time.Minute)
	if removed := r.PruneExpired(); removed != 1 {
		t.Fatalf("PruneExpired() removed = %d, want 1", removed)
	}
	if _, ok := store.rows[first.Session.ID]; !ok {
		t.Fatal("selected failed durable row was removed")
	}
	if _, ok := store.rows[second.Session.ID]; ok {
		t.Fatal("independent expired durable row was blocked by first delete failure")
	}
	r.mu.Lock()
	_, firstRetained := r.sessions[first.Session.ID]
	_, secondRetained := r.sessions[second.Session.ID]
	heapLen := r.expiries.Len()
	r.mu.Unlock()
	if !firstRetained || secondRetained || heapLen != 1 {
		t.Fatalf("retention after selective failure: first=%v second=%v heap=%d", firstRetained, secondRetained, heapLen)
	}
}
