package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestOpen_CustomDir(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "sub", "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open db in custom dir: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping db: %v", err)
	}
}

func TestMigrations_Versioning(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)

	// Verify schema_migrations has version 1, 2, and 3
	rows, err := db.QueryContext(ctx, "SELECT version, description FROM schema_migrations ORDER BY version ASC")
	if err != nil {
		t.Fatalf("failed to query schema_migrations: %v", err)
	}
	defer rows.Close()

	type mig struct {
		version int
		desc    string
	}
	var appliedMigrations []mig
	for rows.Next() {
		var m mig
		if err := rows.Scan(&m.version, &m.desc); err != nil {
			t.Fatalf("failed to scan migration: %v", err)
		}
		appliedMigrations = append(appliedMigrations, m)
	}
	if len(appliedMigrations) != len(migrations) {
		t.Fatalf("expected %d applied migrations, got %d", len(migrations), len(appliedMigrations))
	}
	for i := 0; i < len(appliedMigrations); i++ {
		if appliedMigrations[i].version != i+1 {
			t.Errorf("expected migration index %d to have version %d, got %d", i, i+1, appliedMigrations[i].version)
		}
	}

	// Test legacy adoption
	// Create raw in-memory db simulating legacy database with sudo_users table
	rawDB, err := sql.Open("sqlite", "file:legacy_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("failed to open raw sqlite: %v", err)
	}
	defer rawDB.Close()

	_, err = rawDB.ExecContext(ctx, `
		CREATE TABLE sudo_users (
			user_id INTEGER PRIMARY KEY,
			added_at TIMESTAMP NOT NULL,
			added_by INTEGER NOT NULL
		);
		CREATE TABLE scheduled_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			peer_type TEXT NOT NULL,
			access_hash INTEGER NOT NULL DEFAULT 0,
			action_type TEXT NOT NULL,
			payload TEXT NOT NULL,
			interval_seconds INTEGER NOT NULL DEFAULT 0,
			next_run_at TIMESTAMP NOT NULL,
			created_at TIMESTAMP NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}

	wrapped := &DB{DB: rawDB}
	if err := wrapped.migrate(ctx); err != nil {
		t.Fatalf("migrate failed on legacy db: %v", err)
	}

	// Verify all 9 migrations are recorded
	var count int
	if err := rawDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("failed to count schema_migrations: %v", err)
	}
	if count != len(migrations) {
		t.Fatalf("expected %d migrations in legacy db after runMigrations, got %d", len(migrations), count)
	}
}

func TestMigrations_ChecksumValidation(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)

	// 1. Verify all migrations have non-empty checksums
	rows, err := db.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version ASC")
	if err != nil {
		t.Fatalf("failed to query schema_migrations: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var v int
		var cs string
		if err := rows.Scan(&v, &cs); err != nil {
			t.Fatalf("failed to scan migration: %v", err)
		}
		if cs == "" {
			t.Errorf("expected migration %d to have non-empty checksum", v)
		}
		count++
	}
	if count != len(migrations) {
		t.Fatalf("expected %d migrations, got %d", len(migrations), count)
	}

	// 2. Tampering detection: simulate altered checksum in schema_migrations
	_, err = db.ExecContext(ctx, "UPDATE schema_migrations SET checksum = 'tampered-hash' WHERE version = 1")
	if err != nil {
		t.Fatalf("failed to tamper checksum: %v", err)
	}

	// Running migrate again should detect tampering and return an error
	err = db.migrate(ctx)
	if err == nil {
		t.Fatalf("expected error on tampered migration checksum, got nil")
	}
	if !strings.Contains(err.Error(), "migration checksum mismatch") {
		t.Errorf("expected checksum mismatch error, got: %v", err)
	}

	// 3. Checksum backfilling: if checksum is empty, migrate should backfill it
	_, err = db.ExecContext(ctx, "UPDATE schema_migrations SET checksum = '' WHERE version = 1")
	if err != nil {
		t.Fatalf("failed to clear checksum: %v", err)
	}

	if err := db.migrate(ctx); err != nil {
		t.Fatalf("migrate failed on empty checksum: %v", err)
	}

	var backfilled string
	if err := db.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = 1").Scan(&backfilled); err != nil {
		t.Fatalf("failed to query backfilled checksum: %v", err)
	}
	expected := calculateMigrationChecksum(migrations[0])
	if backfilled != expected {
		t.Errorf("expected backfilled checksum %s, got %s", expected, backfilled)
	}

	// 4. Legacy checksum adoption: version 15 with recorded legacy hash should succeed and normalize
	_, err = db.ExecContext(ctx, "UPDATE schema_migrations SET checksum = 'f23db64a47cbd211704582fac2557e3ad87386c35a61589080fac2f699989182' WHERE version = 15")
	if err != nil {
		t.Fatalf("failed to update checksum for version 15: %v", err)
	}
	if err := db.migrate(ctx); err != nil {
		t.Fatalf("migrate failed on legacy checksum for version 15: %v", err)
	}
}

func TestPeerEntity_SaveAndFindByUsername(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Save peer storage access hash
	query := `INSERT INTO peers_storage (prefix, id, access_hash, updated_at) VALUES ('user', 12345, 987654321, ?)`
	if _, err := db.ExecContext(ctx, query, time.Now()); err != nil {
		t.Fatalf("failed to insert peers_storage: %v", err)
	}

	// 2. Save peer entity metadata
	err := db.SavePeerEntity(ctx, "user", 12345, "@GopherBot", "+1234567890", "Go", "Pher", "")
	if err != nil {
		t.Fatalf("failed to save peer entity: %v", err)
	}

	// 3. Find by username (case-insensitive, with @ prefix)
	prefix, id, accessHash, found, err := db.FindPeerByUsername(ctx, "@gopherbot")
	if err != nil {
		t.Fatalf("FindPeerByUsername failed: %v", err)
	}
	if !found {
		t.Fatalf("expected peer to be found")
	}
	if prefix != "user" || id != 12345 || accessHash != 987654321 {
		t.Errorf("unexpected find result: prefix=%s, id=%d, accessHash=%d", prefix, id, accessHash)
	}

	// 4. Find by username without @ prefix
	_, _, _, foundNoAt, err := db.FindPeerByUsername(ctx, "GOPHERBOT")
	if err != nil || !foundNoAt {
		t.Errorf("expected peer to be found without @: %v, found: %v", err, foundNoAt)
	}

	// 5. Non-existent username
	_, _, _, foundNonExistent, err := db.FindPeerByUsername(ctx, "nobody_here")
	if err != nil {
		t.Fatalf("unexpected error for non-existent user: %v", err)
	}
	if foundNonExistent {
		t.Errorf("expected found=false for non-existent user")
	}
}

func TestModeration_Warnings(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	chatID := int64(-100123)
	userID := int64(456)
	warnedBy := int64(789)

	// 1. Initial count 0
	cnt, err := db.GetWarningCount(ctx, chatID, userID)
	if err != nil {
		t.Fatalf("GetWarningCount error: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("expected 0 warnings, got %d", cnt)
	}

	// 2. Add warnings
	if err := db.AddWarning(ctx, chatID, userID, "rule violation 1", warnedBy); err != nil {
		t.Fatalf("failed to add warning: %v", err)
	}
	if err := db.AddWarning(ctx, chatID, userID, "rule violation 2", warnedBy); err != nil {
		t.Fatalf("failed to add second warning: %v", err)
	}

	// 3. Verify count and list
	cnt, err = db.GetWarningCount(ctx, chatID, userID)
	if err != nil || cnt != 2 {
		t.Errorf("expected 2 warnings, got %d (err=%v)", cnt, err)
	}

	records, err := db.GetWarnings(ctx, chatID, userID)
	if err != nil || len(records) != 2 {
		t.Fatalf("expected 2 records, got %d (err=%v)", len(records), err)
	}
	if records[0].Reason != "rule violation 2" || records[1].Reason != "rule violation 1" {
		t.Errorf("unexpected record order: %+v", records)
	}

	// 4. Reset warnings
	if err := db.ResetWarnings(ctx, chatID, userID); err != nil {
		t.Fatalf("failed to reset warnings: %v", err)
	}
	cnt, err = db.GetWarningCount(ctx, chatID, userID)
	if err != nil || cnt != 0 {
		t.Errorf("expected 0 warnings after reset, got %d", cnt)
	}
}
