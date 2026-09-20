package app

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
)

func TestBuiltinRegistryCompatibilityProgressesWithEphemeralStorage(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateBuiltinFeatures(ctx, db); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO notes (
			chat_id, name, content, response_format,
			media_asset_id, media_type, media_name, media_mime,
			created_at, updated_at
		) VALUES (7, 'ephemeral-registry', 'fallback', 'html',
			'durable-but-unavailable', 'photo', 'image.png', 'image/png', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}

	stats, err := reconcileBuiltinPersistentMedia(ctx, db, storage.NewMemoryStorage())
	if err != nil {
		t.Fatal(err)
	}
	if stats != (savedresponse.PersistentMediaReconcileStats{}) {
		t.Fatalf("ephemeral storage must not run destructive reconciliation: %+v", stats)
	}

	var ledgerCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM saved_response_media_assets
		WHERE asset_id = 'durable-but-unavailable'
	`).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 1 {
		t.Fatalf("legacy ledger count=%d, want 1", ledgerCount)
	}

	registry := mediaregistry.New(db)
	record, err := registry.Asset(ctx, "durable-but-unavailable")
	if err != nil {
		t.Fatal(err)
	}
	if record.Owner != "savedresponse" || record.Producer != "savedresponse.capture" || record.Lifecycle != mediaregistry.LifecyclePersistent {
		t.Fatalf("unexpected registry metadata: %+v", record)
	}
	refs, err := registry.ReferenceCount(ctx, "durable-but-unavailable")
	if err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Fatalf("global reference count=%d, want 1", refs)
	}

	consistency, err := savedresponse.VerifyRegistryCompatibility(ctx, db, startupPersistentMediaReconcileBatch)
	if err != nil {
		t.Fatal(err)
	}
	if !consistency.Consistent() {
		t.Fatalf("compatibility metadata did not converge: %+v", consistency)
	}

	var assetID string
	if err := db.QueryRowContext(ctx, `
		SELECT media_asset_id FROM notes
		WHERE chat_id = 7 AND name = 'ephemeral-registry'
	`).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	if assetID != "durable-but-unavailable" {
		t.Fatalf("ephemeral fallback mutated durable media reference: %q", assetID)
	}
}
