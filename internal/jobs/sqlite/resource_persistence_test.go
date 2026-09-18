package sqlite

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/tasks"
	_ "modernc.org/sqlite"
)

func TestStoreDefinitionResourcesRoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewResourceStore(db)
	want := []tasks.ResourceRequirement{{Name: "process", Amount: 2}, {Name: "media", Amount: 1}}
	def := &jobs.JobDefinition{
		ID:          "job-resource-roundtrip",
		ScopeOwner:  "system",
		QuotaOwner:  "admin",
		HandlerType: "test.resources",
		Version:     1,
		Pool:        "general",
		Class:       "normal",
		Resources:   want,
		Enabled:     true,
	}
	if err := store.SaveDefinition(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetDefinition(context.Background(), def.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Resources, want) {
		t.Fatalf("GetDefinition resources = %#v, want %#v", loaded.Resources, want)
	}
	definitions, err := store.ListDefinitions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || !reflect.DeepEqual(definitions[0].Resources, want) {
		t.Fatalf("ListDefinitions resources = %#v, want %#v", definitions, want)
	}

	next := []tasks.ResourceRequirement{{Name: "gpu", Amount: 3}}
	loaded.Resources = next
	if err := store.UpdateDefinitionCAS(context.Background(), loaded, loaded.Revision); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetDefinition(context.Background(), def.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.Resources, next) {
		t.Fatalf("CAS resources = %#v, want %#v", updated.Resources, next)
	}
}

func TestInitSchemaBackfillsLegacyPoolResourcesOnce(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE job_definitions (
		id TEXT PRIMARY KEY,
		scope_owner TEXT NOT NULL,
		quota_owner TEXT NOT NULL,
		handler_type TEXT NOT NULL,
		version INTEGER NOT NULL DEFAULT 1,
		payload BLOB,
		pool TEXT NOT NULL DEFAULT 'general',
		class TEXT NOT NULL DEFAULT 'normal',
		timeout_ms INTEGER NOT NULL DEFAULT 0,
		retry_policy TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		revision INTEGER NOT NULL DEFAULT 1,
		updated_at DATETIME NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, row := range []struct{ id, pool string }{
		{"legacy-media", "media-process"},
		{"legacy-download", "download"},
		{"legacy-general", "general"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO job_definitions
			(id, scope_owner, quota_owner, handler_type, version, pool, class, retry_policy, enabled, revision, updated_at)
			VALUES (?, 'system', 'system', 'test', 1, ?, 'normal', '', 1, 1, ?)`, row.id, row.pool, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := InitSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewResourceStore(db)
	media, err := store.GetDefinition(ctx, "legacy-media")
	if err != nil {
		t.Fatal(err)
	}
	wantMedia := []tasks.ResourceRequirement{{Name: "process", Amount: 1}, {Name: "media", Amount: 1}}
	if !reflect.DeepEqual(media.Resources, wantMedia) {
		t.Fatalf("media backfill = %#v, want %#v", media.Resources, wantMedia)
	}
	download, err := store.GetDefinition(ctx, "legacy-download")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(download.Resources, []tasks.ResourceRequirement{{Name: "download", Amount: 1}}) {
		t.Fatalf("download backfill = %#v", download.Resources)
	}
	general, err := store.GetDefinition(ctx, "legacy-general")
	if err != nil {
		t.Fatal(err)
	}
	if len(general.Resources) != 0 {
		t.Fatalf("general backfill should remain empty: %#v", general.Resources)
	}

	// Once resources exist, InitSchema must not infer them from pool again.
	if _, err := db.ExecContext(ctx, `UPDATE job_definitions SET resources = '[{"name":"gpu","amount":3}]' WHERE id = 'legacy-media'`); err != nil {
		t.Fatal(err)
	}
	if err := InitSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	media, err = store.GetDefinition(ctx, "legacy-media")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(media.Resources, []tasks.ResourceRequirement{{Name: "gpu", Amount: 3}}) {
		t.Fatalf("explicit resources were overwritten on re-init: %#v", media.Resources)
	}
}
