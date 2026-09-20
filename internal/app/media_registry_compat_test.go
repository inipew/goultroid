package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/storage"
)

func TestBuiltinPersistentMediaReconcileBackfillsGlobalRegistry(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateBuiltinFeatures(ctx, db); err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFileStorage(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.Put(ctx, bytes.NewReader([]byte("persistent")), storage.Metadata{Name: "note.jpg", MIME: "image/jpeg"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO notes (
			chat_id, name, content, response_format,
			media_asset_id, media_type, media_name, media_mime,
			created_at, updated_at
		) VALUES (71, 'startup', 'hello', 'html', ?, 'photo', 'note.jpg', 'image/jpeg', ?, ?)
	`, asset.ID, now, now); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcileBuiltinPersistentMedia(ctx, db, store); err != nil {
		t.Fatal(err)
	}
	registry := mediaregistry.New(db)
	record, err := registry.Asset(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Owner != "savedresponse" || record.Producer != "savedresponse.capture" {
		t.Fatalf("unexpected global media owner: %+v", record)
	}
	refs, err := registry.ReferenceCount(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Fatalf("global reference count=%d, want 1", refs)
	}
}
