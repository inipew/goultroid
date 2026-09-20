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
	failDelete  bool
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
	for _, statement := range []string{
		`ALTER TABLE notes ADD COLUMN media_asset_id TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE notes ADD COLUMN media_type TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE notes ADD COLUMN media_name TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE notes ADD COLUMN media_mime TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE filters ADD COLUMN media_asset_id TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE filters ADD COLUMN media_type TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE filters ADD COLUMN media_name TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE filters ADD COLUMN media_mime TEXT NOT NULL DEFAULT '';`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
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

func TestPreparedIntentIsNotReconciledBeforeGraceAndProtectsLiveAsset(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "live.bin")
	insertReferencedNote(t, db, "live", asset.ID)
	svc := NewService(store, db)

	if err := svc.cleanup.prepare(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := svc.ReconcileCleanup(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != 0 {
		t.Fatalf("prepared intent was visible before grace: %+v", stats)
	}
	pending, err := svc.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("prepared intent count=%d, want 1", pending)
	}

	// Simulate process recovery after the prepared grace window. Because the DB
	// mutation never happened, the asset is still referenced and must survive.
	forceCleanupDue(t, db, asset.ID)
	stats, err = svc.ReconcileCleanup(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Referenced != 1 || stats.Deleted != 0 {
		t.Fatalf("unexpected cleanup stats after grace: %+v", stats)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("referenced asset was deleted: %v", err)
	}
	pending, err = svc.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("stale prepared intent was not cleared: %d", pending)
	}
}

func TestPreparedIntentRecoversCrashAfterDBMutationBeforeActivation(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "post-commit-crash.bin")
	insertReferencedNote(t, db, "crash", asset.ID)
	svc := NewService(store, db)

	if err := svc.cleanup.prepare(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	// Simulate the DB mutation committing, followed by process death before
	// cleanup.activate() can make the intent immediately due.
	if _, err := db.Exec(`UPDATE notes SET media_asset_id = '' WHERE chat_id = 1 AND name = 'crash'`); err != nil {
		t.Fatal(err)
	}
	forceCleanupDue(t, db, asset.ID)

	stats, err := svc.ReconcileCleanup(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 || stats.Referenced != 0 {
		t.Fatalf("unexpected recovery stats: %+v", stats)
	}
	if _, err := store.Stat(context.Background(), asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("orphaned asset survived recovery: %v", err)
	}
	pending, err := svc.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("cleanup intent survived successful recovery: %d", pending)
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


func TestPersistentMediaOrphanDeletionFailsClosedOnIncompleteReferenceSchema(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`ALTER TABLE notes ADD COLUMN media_asset_id TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE notes ADD COLUMN media_type TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE notes ADD COLUMN media_name TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE notes ADD COLUMN media_mime TEXT NOT NULL DEFAULT '';`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "schema-incomplete.bin")
	svc := NewService(store, db)
	if err := svc.assets.register(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}

	stats, err := svc.ReconcilePersistentMedia(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OrphansDiscovered != 0 || stats.Cleanup.Deleted != 0 {
		t.Fatalf("incomplete reference schema must fail closed: %+v", stats)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("tracked asset was deleted while reference schema was incomplete: %v", err)
	}
}

func TestPersistentMediaBackfillSharesBudgetAcrossReferenceSources(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	now := time.Now().UTC()
	for i := 0; i < 6; i++ {
		if _, err := db.Exec(`
			INSERT INTO notes (chat_id, name, content, media_asset_id, created_at, updated_at)
			VALUES (1, ?, 'note', ?, ?, ?)
		`, fmt.Sprintf("fair-note-%02d", i), fmt.Sprintf("note-asset-%02d", i), now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO filters (chat_id, keyword, reply_text, media_asset_id, created_at)
			VALUES (1, ?, 'filter', ?, ?)
		`, fmt.Sprintf("fair-filter-%02d", i), fmt.Sprintf("filter-asset-%02d", i), now); err != nil {
			t.Fatal(err)
		}
	}

	ledger := newAssetLedger(db)
	inserted, err := ledger.backfillReferences(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 4 {
		t.Fatalf("bounded fair backfill inserted=%d, want 4", inserted)
	}
	for _, prefix := range []string{"note-asset-", "filter-asset-"} {
		var count int
		if err := db.QueryRow(`
			SELECT count(*) FROM saved_response_media_assets
			WHERE asset_id LIKE ?
		`, prefix+"%").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatalf("backfill starved reference source prefix %q", prefix)
		}
	}
}

func TestPersistentMediaReconcileRepairsMissingReferencedAssets(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	now := time.Now().UTC()

	_, err := db.Exec(`
		INSERT INTO notes (
			chat_id, name, content, media_asset_id, media_type, media_name, media_mime, created_at, updated_at
		) VALUES (1, 'text-fallback', 'still usable', 'missing-shared', 'photo', 'missing.png', 'image/png', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO filters (
			chat_id, keyword, reply_text, media_asset_id, media_type, media_name, media_mime, created_at
		) VALUES (1, 'media-only', '', 'missing-shared', 'photo', 'missing.png', 'image/png', ?)
	`, now)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(store, db)
	stats, err := svc.ReconcilePersistentMedia(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if stats.MissingAssets != 1 || stats.ReferencesDetached != 1 || stats.ResponsesRemoved != 1 {
		t.Fatalf("unexpected missing-media reconciliation stats: %+v", stats)
	}

	var content, assetID, mediaType, mediaName, mediaMIME string
	if err := db.QueryRow(`
		SELECT content, media_asset_id, media_type, media_name, media_mime
		FROM notes WHERE chat_id = 1 AND name = 'text-fallback'
	`).Scan(&content, &assetID, &mediaType, &mediaName, &mediaMIME); err != nil {
		t.Fatal(err)
	}
	if content != "still usable" || assetID != "" || mediaType != "" || mediaName != "" || mediaMIME != "" {
		t.Fatalf("text fallback note was not repaired: content=%q asset=%q type=%q name=%q mime=%q",
			content, assetID, mediaType, mediaName, mediaMIME)
	}

	var filterCount int
	if err := db.QueryRow(`
		SELECT count(*) FROM filters WHERE chat_id = 1 AND keyword = 'media-only'
	`).Scan(&filterCount); err != nil {
		t.Fatal(err)
	}
	if filterCount != 0 {
		t.Fatalf("missing media-only filter survived reconciliation: %d", filterCount)
	}
	tracked, err := svc.TrackedMediaCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tracked != 0 {
		t.Fatalf("missing asset remained in ledger: %d", tracked)
	}
}

func TestPersistentMediaBackfillIsBounded(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		if _, err := db.Exec(`
			INSERT INTO notes (chat_id, name, content, media_asset_id, created_at, updated_at)
			VALUES (1, ?, 'text', ?, ?, ?)
		`, fmt.Sprintf("note-%02d", i), fmt.Sprintf("asset-%02d", i), now, now); err != nil {
			t.Fatal(err)
		}
	}

	ledger := newAssetLedger(db)
	inserted, err := ledger.backfillReferences(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 3 {
		t.Fatalf("bounded backfill inserted=%d, want 3", inserted)
	}
	count, err := ledger.count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("bounded backfill ledger count=%d, want 3", count)
	}
}

func TestPersistentMediaReconcileBackfillsLiveReferencesAndDeletesTrackedOrphan(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	live := putCleanupTestAsset(t, store, "live-global.bin")
	orphan := putCleanupTestAsset(t, store, "orphan-global.bin")
	untracked := putCleanupTestAsset(t, store, "shared-media.bin")
	insertReferencedNote(t, db, "global-live", live.ID)

	svc := NewService(store, db)
	if err := svc.assets.register(context.Background(), orphan.ID); err != nil {
		t.Fatal(err)
	}

	stats, err := svc.ReconcilePersistentMedia(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ReferencesBackfilled != 1 || stats.OrphansDiscovered != 1 || stats.CleanupScheduled != 1 {
		t.Fatalf("unexpected persistent reconciliation stats: %+v", stats)
	}
	if stats.Cleanup.Deleted != 1 {
		t.Fatalf("orphan cleanup stats=%+v, want one deletion", stats.Cleanup)
	}
	if _, err := store.Stat(context.Background(), live.ID); err != nil {
		t.Fatalf("live referenced asset disappeared: %v", err)
	}
	if _, err := store.Stat(context.Background(), orphan.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("tracked orphan survived reconciliation: %v", err)
	}
	if _, err := store.Stat(context.Background(), untracked.ID); err != nil {
		t.Fatalf("untracked shared-storage asset must remain untouched: %v", err)
	}
	tracked, err := svc.TrackedMediaCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tracked != 1 {
		t.Fatalf("tracked media count=%d, want only live referenced asset", tracked)
	}
}

func TestPersistentMediaReconcileDoesNotResetCleanupBackoff(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "backoff-orphan.bin")
	svc := NewService(store, db)
	if err := svc.assets.register(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.cleanup.enqueue(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	item := cleanupItem{AssetID: asset.ID, Attempts: 2}
	if err := svc.cleanup.recordFailure(context.Background(), item, errors.New("keep backoff")); err != nil {
		t.Fatal(err)
	}

	var beforeAttempts int
	var beforeNext time.Time
	if err := db.QueryRow(`
		SELECT attempts, next_attempt_at
		FROM saved_response_media_cleanup
		WHERE asset_id = ?
	`, asset.ID).Scan(&beforeAttempts, &beforeNext); err != nil {
		t.Fatal(err)
	}

	stats, err := svc.ReconcilePersistentMedia(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OrphansDiscovered != 1 || stats.CleanupScheduled != 0 || stats.Cleanup.Scanned != 0 {
		t.Fatalf("reconcile unexpectedly reset/dequeued backoff: %+v", stats)
	}

	var afterAttempts int
	var afterNext time.Time
	if err := db.QueryRow(`
		SELECT attempts, next_attempt_at
		FROM saved_response_media_cleanup
		WHERE asset_id = ?
	`, asset.ID).Scan(&afterAttempts, &afterNext); err != nil {
		t.Fatal(err)
	}
	if afterAttempts != beforeAttempts || !afterNext.Equal(beforeNext) {
		t.Fatalf("cleanup backoff changed: before attempts=%d next=%v, after attempts=%d next=%v",
			beforeAttempts, beforeNext, afterAttempts, afterNext)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("asset was deleted before retry deadline: %v", err)
	}
}

func TestPersistentMediaStartupReconcileRecoversFreshCrashOrphan(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "fresh-crash-orphan.bin")
	svc := NewService(store, db)
	if err := svc.assets.register(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.cleanup.prepare(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}

	stats, err := svc.ReconcilePersistentMedia(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OrphansDiscovered != 1 || stats.CleanupScheduled != 1 || stats.Cleanup.Deleted != 1 {
		t.Fatalf("fresh crash orphan was not reclaimed at startup: %+v", stats)
	}
	if _, err := store.Stat(context.Background(), asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("fresh crash orphan survived startup reconciliation: %v", err)
	}
}

func TestGlobalMigrationCreatesPersistentMediaLedger(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var table string
	if err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = 'saved_response_media_assets'
	`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table != "saved_response_media_assets" {
		t.Fatalf("persistent media ledger table=%q", table)
	}
	var indexCount int
	if err := db.QueryRow(`
		SELECT count(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_saved_response_media_assets_registered'
	`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatalf("persistent media ledger index count=%d", indexCount)
	}
}

func TestPersistentMediaReconcileRecoversOutOfBandReferenceLoss(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "out-of-band.bin")
	insertReferencedNote(t, db, "out-of-band", asset.ID)
	svc := NewService(store, db)

	first, err := svc.ReconcilePersistentMedia(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReferencesBackfilled != 1 || first.Cleanup.Deleted != 0 {
		t.Fatalf("unexpected first reconciliation: %+v", first)
	}

	if _, err := db.Exec(`DELETE FROM notes WHERE chat_id = 1 AND name = 'out-of-band'`); err != nil {
		t.Fatal(err)
	}
	second, err := svc.ReconcilePersistentMedia(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if second.OrphansDiscovered != 1 || second.CleanupScheduled != 1 || second.Cleanup.Deleted != 1 {
		t.Fatalf("out-of-band reference loss was not reconciled: %+v", second)
	}
	if _, err := store.Stat(context.Background(), asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("out-of-band orphan survived reconciliation: %v", err)
	}
}

func TestCommitReplacementDisarmsPreparedCaptureIntent(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "commit-live.bin")
	svc := NewService(store, db)

	if err := svc.assets.register(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.cleanup.prepare(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	next := Response{Text: "live", Media: &MediaRef{AssetID: asset.ID}}
	if err := svc.CommitReplacement(context.Background(), Response{}, next, func() error {
		insertReferencedNote(t, db, "commit-live", asset.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	pending, err := svc.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("prepared capture intent survived durable commit: %d", pending)
	}
	tracked, err := svc.TrackedMediaCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tracked != 1 {
		t.Fatalf("tracked media count=%d, want 1", tracked)
	}
	if _, err := store.Stat(context.Background(), asset.ID); err != nil {
		t.Fatalf("committed media asset disappeared: %v", err)
	}
}

func TestDeleteMediaRemovesLedgerAndPreparedIntent(t *testing.T) {
	db := openCleanupTestDB(t)
	defer db.Close()
	store := storage.NewMemoryStorage()
	asset := putCleanupTestAsset(t, store, "validation-failure.bin")
	svc := NewService(store, db)

	if err := svc.assets.register(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.cleanup.prepare(context.Background(), asset.ID); err != nil {
		t.Fatal(err)
	}
	response := Response{Media: &MediaRef{AssetID: asset.ID}}
	if err := svc.DeleteMedia(context.Background(), response); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Stat(context.Background(), asset.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("direct media delete left storage asset: %v", err)
	}
	tracked, err := svc.TrackedMediaCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tracked != 0 {
		t.Fatalf("direct media delete left %d ledger rows", tracked)
	}
	pending, err := svc.PendingCleanupCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("direct media delete left %d cleanup intents", pending)
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
