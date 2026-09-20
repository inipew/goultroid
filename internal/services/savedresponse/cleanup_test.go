package savedresponse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/storage"
)

type failingDeleteStorage struct {
	storage.Storage
	failDelete bool
	deleteCalls int
}

func (s *failingDeleteStorage) Delete(ctx context.Context, id string) error {
	s.deleteCalls++
	if s.failDelete {
		return errors.New("injected delete failure")
	}
	return s.Storage.Delete(ctx, id)
}

func openCleanupTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE notes ADD COLUMN media_asset_id TEXT NOT NULL DEFAULT '';`); err != nil {
		t.Fatal(err)
	}
	return db
}

func putCleanupTestAsset(t *testing.T, store storage.Storage, name string) *storage.Asset {
	t.Helper()
	asset, err := store.Put(context.Background(), strings.NewReader("payload-"+name), storage.Metadata{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func insertReferencedNote(t *testing.T, db *database.DB, name, assetID string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := db.Exec(`
		INSERT INTO notes (chat_id, name, content, media_asset_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, int64(1), name, "content", assetID, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func forceCleanupDue(t *testing.T, db *database.DB, assetID string) {
	t.Helper()
	_, err := db.Exec(`
		UPDATE saved_response_media_cleanup
		SET next_attempt_at = ?
		WHERE asset_id = ?
	`, time.Now().UTC().Add(-time.Second), assetID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommitDeleteKeepsDurableIntentWhenStorageDeleteFails(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	base := storage.NewMemoryStorage()
	failing := &failingDeleteStorage{Storage: base, failDelete: true}
	asset := putCleanupTestAsset(t, base, "delete.bin")
	insertReferencedNote(t, db, "delete", asset.ID)
	svc := NewService(failing, db)
	response := Response{Media: &MediaRef{AssetID: asset.ID}}

	err := svc.CommitDelete(context.Background(), response, func() error {
		_, err := db.Exec(`DELETE FROM notes WHERE chat_id = 1 AND name = 'delete'`)
		return err
	})
	if err != nil {
		t.Fatalf("CommitDelete returned cleanup failure after durable mutation: %v", err)
	}
	var noteCount int
	if err := db.QueryRow(`SELECT count(*) FROM notes WHERE chat_id = 1 AND name = 'delete'`).Scan(&noteCount); err != nil {
		t.Fatal(err)
	}
	if noteCount != 0 {
		t.Fatalf("note count=%d, want deleted DB row", noteCount)
	}
	pending, err := svc.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending cleanup=%d, want 1", pending)
	}
	if _, err := base.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("asset disappeared despite injected delete failure: %v", err)
	}

	failing.failDelete = false
	forceCleanupDue(t, db, asset.ID)
	stats, err := svc.ReconcileCleanup(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 || stats.Deferred != 0 {
		t.Fatalf("unexpected cleanup stats: %+v", stats)
	}
	pending, err = svc.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending cleanup=%d after retry, want 0", pending)
	}
	if _, err := base.Stat(context.Background(), asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("asset still exists after successful retry: %v", err)
	}
}

func TestReconcileDropsIntentWithoutDeletingStillReferencedAsset(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "live.bin")
	insertReferencedNote(t, db, "live", asset.ID)
	svc := NewService(store, db)

	if err := svc.cleanup.enqueue(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := svc.ReconcileCleanup(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Referenced != 1 || stats.Deleted != 0 {
		t.Fatalf("unexpected cleanup stats: %+v", stats)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("referenced asset was deleted: %v", err)
	}
	pending, err := svc.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("stale pre-commit intent was not cleared: %d", pending)
	}
}

func TestCommitReplacementPersistFailureKeepsOldAssetAndCleansNew(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	oldAsset := putCleanupTestAsset(t, store, "old.bin")
	newAsset := putCleanupTestAsset(t, store, "new.bin")
	insertReferencedNote(t, db, "replace", oldAsset.ID)
	svc := NewService(store, db)
	previous := Response{Media: &MediaRef{AssetID: oldAsset.ID}}
	next := Response{Media: &MediaRef{AssetID: newAsset.ID}}
	want := errors.New("persist failed")

	err := svc.CommitReplacement(context.Background(), previous, next, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("CommitReplacement error=%v, want %v", err, want)
	}
	if _, err := store.Stat(context.Background(), oldAsset.ID); err != nil {
		t.Fatalf("old referenced asset was removed: %v", err)
	}
	if _, err := store.Stat(context.Background(), newAsset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("new uncommitted asset still exists: %v", err)
	}
	pending, err := svc.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending cleanup=%d, want 0 after successful uncommitted cleanup", pending)
	}
}

func TestCleanupRetryDelayIsExponentiallyBounded(t *testing.T) {
	if got := cleanupRetryDelay(1); got != 5*time.Second {
		t.Fatalf("attempt 1 delay=%v", got)
	}
	if got := cleanupRetryDelay(2); got != 10*time.Second {
		t.Fatalf("attempt 2 delay=%v", got)
	}
	if got := cleanupRetryDelay(100); got > time.Hour {
		t.Fatalf("retry delay exceeded one hour: %v", got)
	}
}

func TestGlobalMigrationCreatesSavedResponseCleanupJournal(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var table string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'saved_response_media_cleanup'`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table != "saved_response_media_cleanup" {
		t.Fatalf("cleanup table=%q", table)
	}
	var indexCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_saved_response_media_cleanup_due'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatalf("cleanup due index count=%d", indexCount)
	}
}

func TestCleanupJournalSkipsMissingFeatureColumns(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "legacy-schema.bin")
	svc := NewService(store, db)
	if err := svc.cleanup.enqueue(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := svc.ReconcileCleanup(context.Background(), 16)
	if err != nil {
		t.Fatalf("reconcile on legacy columns failed: %v", err)
	}
	if stats.Deleted != 1 {
		t.Fatalf("unexpected cleanup stats: %+v", stats)
	}
}

func TestCleanupErrorIsTruncatedBeforePersistence(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	journal := newCleanupJournal(db)
	item := cleanupItem{AssetID: "asset-x"}
	if err := journal.enqueue(context.Background(), item.AssetID); err != nil {
		t.Fatal(err)
	}
	cause := fmt.Errorf("%s", strings.Repeat("x", maxCleanupErrorLen+200))
	if err := journal.recordFailure(context.Background(), item, cause); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT last_error FROM saved_response_media_cleanup WHERE asset_id = ?`, item.AssetID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != maxCleanupErrorLen {
		t.Fatalf("stored error length=%d, want %d", len(stored), maxCleanupErrorLen)
	}
}
