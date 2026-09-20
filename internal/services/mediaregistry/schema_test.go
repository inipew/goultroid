package mediaregistry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
)

func TestSchemaReadyRejectsPartialRegistry(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ready, err := mediaregistry.SchemaReady(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("absent registry unexpectedly reported ready")
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE media_assets (asset_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	ready, err = mediaregistry.SchemaReady(ctx, db)
	if ready {
		t.Fatal("partial registry unexpectedly reported ready")
	}
	if !errors.Is(err, mediaregistry.ErrIncompleteSchema) {
		t.Fatalf("partial registry error=%v, want ErrIncompleteSchema", err)
	}
}
