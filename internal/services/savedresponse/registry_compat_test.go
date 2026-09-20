package savedresponse_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	filtersplugin "github.com/inipew/goultroid/plugins/filters"
	notesplugin "github.com/inipew/goultroid/plugins/notes"
)

func newRegistryCompatibilityDB(t *testing.T) *database.DB {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(
		ctx,
		db,
		mediaregistry.MigrationProvider{},
		notesplugin.Module,
		filtersplugin.Module,
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertLegacyAsset(t *testing.T, db *database.DB, assetID string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO saved_response_media_assets (asset_id, registered_at, last_seen_at)
		VALUES (?, ?, ?)
	`, assetID, now, now); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryCompatibilityBackfillsNotesAndFiltersIdempotently(t *testing.T) {
	ctx := context.Background()
	db := newRegistryCompatibilityDB(t)
	now := time.Now().UTC()

	insertLegacyAsset(t, db, "legacy-note")
	insertLegacyAsset(t, db, "legacy-filter")
	if _, err := db.ExecContext(ctx, `
		INSERT INTO notes (
			chat_id, name, content, response_format,
			media_asset_id, media_type, media_name, media_mime,
			created_at, updated_at
		) VALUES (11, 'welcome', 'hello', 'html', 'legacy-note', 'photo', 'note.jpg', 'image/jpeg', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO filters (
			chat_id, keyword, reply_text, response_format,
			media_asset_id, media_type, media_name, media_mime, created_at
		) VALUES (22, 'ping', 'pong', 'html', 'legacy-filter', 'photo', 'filter.jpg', 'image/jpeg', ?)
	`, now); err != nil {
		t.Fatal(err)
	}

	stats, err := savedresponse.ReconcileRegistryCompatibility(ctx, db, 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AssetsBackfilled != 2 {
		t.Fatalf("assets backfilled=%d, want 2", stats.AssetsBackfilled)
	}
	if stats.ReferencesBackfilled != 2 {
		t.Fatalf("references backfilled=%d, want 2", stats.ReferencesBackfilled)
	}
	if !stats.Consistency.Consistent() {
		t.Fatalf("compatibility reconciliation left divergence: %+v", stats.Consistency)
	}

	registry := mediaregistry.New(db)
	for _, assetID := range []string{"legacy-note", "legacy-filter"} {
		record, err := registry.Asset(ctx, assetID)
		if err != nil {
			t.Fatal(err)
		}
		if record.Owner != "savedresponse" || record.Producer != "savedresponse.capture" || record.Lifecycle != mediaregistry.LifecyclePersistent {
			t.Fatalf("unexpected registration for %s: %+v", assetID, record)
		}
		refs, err := registry.ReferenceCount(ctx, assetID)
		if err != nil {
			t.Fatal(err)
		}
		if refs != 1 {
			t.Fatalf("reference count for %s=%d, want 1", assetID, refs)
		}
	}

	second, err := savedresponse.ReconcileRegistryCompatibility(ctx, db, 16)
	if err != nil {
		t.Fatal(err)
	}
	if second.AssetsBackfilled != 0 || second.ReferencesBackfilled != 0 || second.StaleReferencesRemoved != 0 || second.StaleAssetsRemoved != 0 {
		t.Fatalf("second compatibility pass was not idempotent: %+v", second)
	}
	if !second.Consistency.Consistent() {
		t.Fatalf("second pass consistency mismatch: %+v", second.Consistency)
	}
}

func TestRegistryCompatibilityRepairsStaleMirrorMetadataOnly(t *testing.T) {
	ctx := context.Background()
	db := newRegistryCompatibilityDB(t)
	insertLegacyAsset(t, db, "live")

	if err := mediaregistry.RegisterAssetWithExecutor(
		ctx,
		db,
		savedresponse.MediaRegistryAssetRegistration("live"),
		mediaregistry.Reference{AssetID: "live", Subsystem: "notes", Kind: "note", Key: "9:deleted"},
	); err != nil {
		t.Fatal(err)
	}
	if err := mediaregistry.RegisterAssetWithExecutor(
		ctx,
		db,
		savedresponse.MediaRegistryAssetRegistration("stale-asset"),
	); err != nil {
		t.Fatal(err)
	}

	stats, err := savedresponse.ReconcileRegistryCompatibility(ctx, db, 16)
	if err != nil {
		t.Fatal(err)
	}
	if stats.StaleReferencesRemoved != 1 {
		t.Fatalf("stale references removed=%d, want 1", stats.StaleReferencesRemoved)
	}
	if stats.StaleAssetsRemoved != 1 {
		t.Fatalf("stale assets removed=%d, want 1", stats.StaleAssetsRemoved)
	}
	if !stats.Consistency.Consistent() {
		t.Fatalf("metadata repair left divergence: %+v", stats.Consistency)
	}
	if _, err := mediaregistry.New(db).Asset(ctx, "stale-asset"); !errors.Is(err, mediaregistry.ErrAssetNotRegistered) {
		t.Fatalf("stale registry asset still present: %v", err)
	}
}

func TestRegistryCompatibilityMakesBoundedProgress(t *testing.T) {
	ctx := context.Background()
	db := newRegistryCompatibilityDB(t)
	for _, assetID := range []string{"asset-a", "asset-b", "asset-c"} {
		insertLegacyAsset(t, db, assetID)
	}

	first, err := savedresponse.ReconcileRegistryCompatibility(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.AssetsBackfilled != 1 {
		t.Fatalf("first pass backfilled %d assets, want 1", first.AssetsBackfilled)
	}
	if first.Consistency.MissingGlobalAssets != 1 || !first.Consistency.FindingsTruncated {
		t.Fatalf("expected bounded verification finding after first pass: %+v", first.Consistency)
	}

	for i := 0; i < 2; i++ {
		if _, err := savedresponse.ReconcileRegistryCompatibility(ctx, db, 1); err != nil {
			t.Fatal(err)
		}
	}
	final, err := savedresponse.VerifyRegistryCompatibility(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !final.Consistent() {
		t.Fatalf("bounded passes did not converge: %+v", final)
	}
}
