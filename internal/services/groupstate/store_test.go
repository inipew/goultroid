package groupstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

func newTestStore(t *testing.T, limits Limits) (*SQLiteStore, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, MigrationProvider{}); err != nil {
		t.Fatalf("RunFeatureMigrations() error=%v", err)
	}
	store, err := NewSQLiteStoreWithLimits(db, limits)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func TestSQLiteStoreCASRestartAndChatIsolation(t *testing.T) {
	store, db := newTestStore(t, Limits{MaxEntries: 8, CleanupBatch: 2})
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }

	created, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 100, Namespace: " Moderation ", Key: " Mode "},
		Value:         []byte("strict"),
		UpdatedBy:     42,
		UpdatedAt:     base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.Namespace != "moderation" || created.Key != "mode" || string(created.Value) != "strict" {
		t.Fatalf("created=%+v", created)
	}

	if _, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 100, Namespace: "moderation", Key: "mode"},
		Value:         []byte("duplicate-create"),
		UpdatedBy:     42,
		UpdatedAt:     base,
	}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("duplicate create error=%v, want ErrStateConflict", err)
	}

	updated, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey:    created.GroupStateKey,
		ExpectedRevision: created.Revision,
		Value:            []byte("relaxed"),
		UpdatedBy:        43,
		UpdatedAt:        base.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.UpdatedBy != 43 || string(updated.Value) != "relaxed" {
		t.Fatalf("updated=%+v", updated)
	}

	if _, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey:    created.GroupStateKey,
		ExpectedRevision: 1,
		Value:            []byte("stale"),
		UpdatedBy:        44,
		UpdatedAt:        base.Add(2 * time.Minute),
	}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale CAS error=%v, want ErrStateConflict", err)
	}

	otherChat, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 200, Namespace: "moderation", Key: "mode"},
		Value:         []byte("other"),
		UpdatedBy:     42,
		UpdatedAt:     base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if otherChat.Revision != 1 {
		t.Fatalf("other chat revision=%d, want 1", otherChat.Revision)
	}

	restarted, err := NewSQLiteStoreWithLimits(db, Limits{MaxEntries: 8, CleanupBatch: 2})
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = store.now
	got, err := restarted.Get(ctx, core.GroupStateKey{ChatID: 100, Namespace: "MODERATION", Key: "MODE"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || string(got.Value) != "relaxed" {
		t.Fatalf("restart state=%+v", got)
	}
	other, err := restarted.Get(ctx, core.GroupStateKey{ChatID: 200, Namespace: "moderation", Key: "mode"})
	if err != nil {
		t.Fatal(err)
	}
	if string(other.Value) != "other" {
		t.Fatalf("chat isolation state=%+v", other)
	}
}

func TestSQLiteStoreDeleteUsesExactRevision(t *testing.T) {
	store, _ := newTestStore(t, Limits{MaxEntries: 8, CleanupBatch: 2})
	ctx := context.Background()
	now := time.Now().UTC()
	created, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 1, Namespace: "filters", Key: "enabled"},
		Value:         []byte("true"),
		UpdatedBy:     7,
		UpdatedAt:     now,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteCompareAndSwap(ctx, core.GroupStateDelete{
		GroupStateKey:    created.GroupStateKey,
		ExpectedRevision: created.Revision + 1,
		DeletedBy:        7,
		DeletedAt:        now,
	}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale delete error=%v, want ErrStateConflict", err)
	}
	if _, err := store.Get(ctx, created.GroupStateKey); err != nil {
		t.Fatalf("stale delete removed state: %v", err)
	}

	if err := store.DeleteCompareAndSwap(ctx, core.GroupStateDelete{
		GroupStateKey:    created.GroupStateKey,
		ExpectedRevision: created.Revision,
		DeletedBy:        7,
		DeletedAt:        now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, created.GroupStateKey); !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("get after delete error=%v, want ErrStateNotFound", err)
	}
}

func TestSQLiteStoreCapacityLazilyReclaimsOnlyExpiredState(t *testing.T) {
	store, _ := newTestStore(t, Limits{MaxEntries: 2, CleanupBatch: 1})
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC)
	now := base
	store.now = func() time.Time { return now }

	expiry := base.Add(time.Minute)
	expiring, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 1, Namespace: "manager", Key: "temporary"},
		Value:         []byte("old"),
		UpdatedBy:     7,
		UpdatedAt:     base,
		ExpiresAt:     &expiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	live, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 2, Namespace: "manager", Key: "durable"},
		Value:         []byte("keep"),
		UpdatedBy:     7,
		UpdatedAt:     base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := store.Count(ctx); err != nil || count != 2 {
		t.Fatalf("initial count=%d err=%v", count, err)
	}

	now = base.Add(2 * time.Minute)
	if _, err := store.Get(ctx, expiring.GroupStateKey); !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("expired get error=%v, want ErrStateNotFound", err)
	}
	if _, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 3, Namespace: "manager", Key: "new"},
		Value:         []byte("new"),
		UpdatedBy:     8,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("create after lazy expiry reclamation: %v", err)
	}
	if count, err := store.Count(ctx); err != nil || count != 2 {
		t.Fatalf("count after reclamation=%d err=%v", count, err)
	}
	if got, err := store.Get(ctx, live.GroupStateKey); err != nil || string(got.Value) != "keep" {
		t.Fatalf("live state was evicted: %+v err=%v", got, err)
	}

	if _, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 4, Namespace: "manager", Key: "overflow"},
		Value:         []byte("no"),
		UpdatedBy:     8,
		UpdatedAt:     now,
	}); !errors.Is(err, ErrStateCapacity) {
		t.Fatalf("capacity error=%v, want ErrStateCapacity", err)
	}
}

func TestSQLiteStorePruneExpiredIsBounded(t *testing.T) {
	store, _ := newTestStore(t, Limits{MaxEntries: 4, CleanupBatch: 1})
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	now := base
	store.now = func() time.Time { return now }

	for chatID := int64(1); chatID <= 2; chatID++ {
		expiry := base.Add(time.Minute)
		if _, err := store.CompareAndSwap(ctx, core.GroupStateCAS{
			GroupStateKey: core.GroupStateKey{ChatID: chatID, Namespace: "temp", Key: "x"},
			Value:         []byte("x"),
			UpdatedBy:     7,
			UpdatedAt:     base,
			ExpiresAt:     &expiry,
		}); err != nil {
			t.Fatal(err)
		}
	}
	now = base.Add(2 * time.Minute)

	pruned, err := store.PruneExpired(ctx, now, 1)
	if err != nil || pruned != 1 {
		t.Fatalf("PruneExpired(limit=1)=%d err=%v", pruned, err)
	}
	if count, err := store.Count(ctx); err != nil || count != 1 {
		t.Fatalf("count after bounded prune=%d err=%v", count, err)
	}
	pruned, err = store.PruneExpired(ctx, now, 1)
	if err != nil || pruned != 1 {
		t.Fatalf("second PruneExpired=%d err=%v", pruned, err)
	}
}

func TestSQLiteStoreRejectsOversizedValueAndInvalidLimits(t *testing.T) {
	store, _ := newTestStore(t, Limits{MaxEntries: 8, CleanupBatch: 2})
	_, err := store.CompareAndSwap(context.Background(), core.GroupStateCAS{
		GroupStateKey: core.GroupStateKey{ChatID: 1, Namespace: "manager", Key: "large"},
		Value:         make([]byte, core.MaxGroupStateValueBytes+1),
		UpdatedBy:     7,
	})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("oversized value error=%v, want ErrInvalidState", err)
	}
	if _, err := NewSQLiteStoreWithLimits(store.db, Limits{MaxEntries: HardMaxEntries + 1}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalid max entries error=%v, want ErrInvalidState", err)
	}
}
