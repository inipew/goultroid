package app

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/module"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
)

func TestBuiltinModulesHaveUniqueDeterministicIDs(t *testing.T) {
	if len(builtinModules) == 0 {
		t.Fatal("builtin module registry is empty")
	}

	ids := make([]string, 0, len(builtinModules))
	seen := make(map[string]struct{}, len(builtinModules))
	for _, m := range builtinModules {
		if err := module.Validate(m); err != nil {
			t.Fatalf("invalid builtin module: %v", err)
		}
		id := m.Manifest().ID
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate builtin module ID %q", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(ids, sorted) {
		t.Fatalf("generated builtin module order is not deterministic by ID: got %v, want %v", ids, sorted)
	}
}

func TestBuiltinModulesResolveInDependencyOrder(t *testing.T) {
	ordered, err := module.ResolveOrder(builtinModules)
	if err != nil {
		t.Fatalf("resolve builtin module dependencies: %v", err)
	}
	if len(ordered) != len(builtinModules) {
		t.Fatalf("resolved %d modules, registered %d", len(ordered), len(builtinModules))
	}

	position := make(map[string]int, len(ordered))
	for i, m := range ordered {
		position[m.Manifest().ID] = i
	}
	for _, m := range ordered {
		for _, dep := range m.Manifest().Dependencies {
			if position[dep] >= position[m.Manifest().ID] {
				t.Fatalf("module %q appears before dependency %q", m.Manifest().ID, dep)
			}
		}
	}
}

func TestBuiltinFeatureMigrationsIncludeGlobalMediaRegistry(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateBuiltinFeatures(ctx, db); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"media_assets", "media_asset_references"} {
		var count int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?
		`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("global media registry table %q missing after builtin migrations", table)
		}
	}
}

func TestBuiltinFeatureMigrationsIncludePMRelaySchema(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateBuiltinFeatures(ctx, db); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{
		"pm_relay_mappings",
		"pm_relay_deliveries",
		"assistant_audience_members",
		"pm_relay_visitor_blocks",
		"assistant_audience_membership_order",
		"pm_relay_force_sub_config",
	} {
		var count int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?
		`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("PM relay table %q missing after builtin migrations", table)
		}
	}
}

func TestBuiltinPersistentMediaReconcileRunsAfterFeatureMigrations(t *testing.T) {
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
		) VALUES (1, 'startup-missing', 'fallback', 'html',
			'missing-startup', 'photo', 'missing.png', 'image/png', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFileStorage(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := reconcileBuiltinPersistentMedia(ctx, db, store)
	if err != nil {
		t.Fatal(err)
	}
	if stats.MissingAssets != 1 || stats.ReferencesDetached != 1 {
		t.Fatalf("unexpected startup reconciliation stats: %+v", stats)
	}

	var content, assetID string
	if err := db.QueryRowContext(ctx, `
		SELECT content, media_asset_id
		FROM notes WHERE chat_id = 1 AND name = 'startup-missing'
	`).Scan(&content, &assetID); err != nil {
		t.Fatal(err)
	}
	if content != "fallback" || assetID != "" {
		t.Fatalf("startup reconciliation did not preserve text fallback: content=%q asset=%q", content, assetID)
	}
}

func TestBuiltinPersistentMediaReconcileSkipsEphemeralFallbackStorage(t *testing.T) {
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
		) VALUES (1, 'ephemeral-guard', 'fallback', 'html',
			'durable-but-unavailable', 'photo', 'image.png', 'image/png', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}

	stats, err := reconcileBuiltinPersistentMedia(ctx, db, storage.NewMemoryStorage())
	if err != nil {
		t.Fatal(err)
	}
	if stats != (savedresponse.PersistentMediaReconcileStats{}) {
		t.Fatalf("ephemeral startup storage should skip reconciliation: %+v", stats)
	}

	var assetID string
	if err := db.QueryRowContext(ctx, `
		SELECT media_asset_id FROM notes
		WHERE chat_id = 1 AND name = 'ephemeral-guard'
	`).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	if assetID != "durable-but-unavailable" {
		t.Fatalf("ephemeral fallback mutated durable media reference: %q", assetID)
	}
}


func TestBuiltinFeatureMigrationsIncludeAssistantGroupState(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateBuiltinFeatures(ctx, db); err != nil {
		t.Fatal(err)
	}

	for _, item := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "assistant_group_state"},
		{kind: "index", name: "idx_assistant_group_state_expiry"},
	} {
		var count int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?
		`, item.kind, item.name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("P7-F %s %q missing after builtin migrations", item.kind, item.name)
		}
	}
}
