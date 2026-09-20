package filters

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

func TestFiltersFeatureMigrationFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("run filters feature migration: %v", err)
	}

	repo := NewSQLiteRepository(db)
	if err := repo.SaveFilter(ctx, 100, "hello", savedresponse.NewText("world")); err != nil {
		t.Fatalf("save filter: %v", err)
	}

	f, err := repo.GetFilter(ctx, 100, "hello")
	if err != nil {
		t.Fatalf("get filter: %v", err)
	}
	if f == nil || f.Response.Text != "world" || f.Response.Format != savedresponse.FormatHTML {
		t.Fatalf("unexpected filter: %#v", f)
	}

	list, err := repo.ListFilters(ctx, 100)
	if err != nil {
		t.Fatalf("list filters: %v", err)
	}
	if len(list) != 1 || list[0].Keyword != "hello" {
		t.Fatalf("unexpected list: %#v", list)
	}

	if err := repo.DeleteFilter(ctx, 100, "hello"); err != nil {
		t.Fatalf("delete filter: %v", err)
	}
}

func TestFiltersFeatureMigrationAdoptLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Legacy version 1 creates filters table
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("adopt filters legacy migration: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM feature_schema_migrations WHERE id = 'filters.001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 adopted feature migration, got %d", count)
	}
}

func TestFiltersRichResponseMetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	response := savedresponse.NewPlainText("literal <tag> & {name}")
	response.Media = &savedresponse.MediaRef{
		AssetID: "asset-filter", MediaType: "sticker", Name: "sticker.webp", MIMEType: "image/webp",
	}
	if err := repo.SaveFilter(ctx, 8, "hello", response); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetFilter(ctx, 8, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Response.Format != savedresponse.FormatPlain || got.Response.Text != response.Text {
		t.Fatalf("unexpected rich filter response: %+v", got)
	}
	if got.Response.Media == nil || *got.Response.Media != *response.Media {
		t.Fatalf("unexpected rich filter media: %+v", got.Response.Media)
	}
}

func TestFiltersMediaReferenceIndexMigration(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_filters_media_asset_id_trim'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("filters media reference index count=%d, want 1", count)
	}
}
