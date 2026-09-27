package idempotency

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLiteExecutionClaimLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db)
	ctx := context.Background()
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(time.Hour, repo)

	claim, isNew, err := mgr.Begin(ctx, "cmd:1", time.Minute)
	if err != nil || !isNew {
		t.Fatalf("begin processing claim: isNew=%v err=%v", isNew, err)
	}

	var status, token string
	if err := db.QueryRowContext(ctx, `SELECT status, claim_token FROM idempotency_keys WHERE key = ?`, "cmd:1").Scan(&status, &token); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || token == "" {
		t.Fatalf("processing row status=%q token=%q", status, token)
	}

	if duplicate, duplicateNew, duplicateErr := mgr.Begin(ctx, "cmd:1", time.Minute); duplicateErr != nil || duplicateNew || duplicate != nil {
		t.Fatalf("duplicate processing claim: claim=%v isNew=%v err=%v", duplicate, duplicateNew, duplicateErr)
	}

	if err := claim.Release(ctx); err != nil {
		t.Fatalf("release processing claim: %v", err)
	}
	if processed, err := mgr.IsProcessedContext(ctx, "cmd:1"); err != nil || processed {
		t.Fatalf("released claim still processed=%v err=%v", processed, err)
	}

	claim, isNew, err = mgr.Begin(ctx, "cmd:1", time.Minute)
	if err != nil || !isNew {
		t.Fatalf("retry claim after release: isNew=%v err=%v", isNew, err)
	}
	if err := claim.Accept(ctx, 5*time.Minute); err != nil {
		t.Fatalf("accept processing claim: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, claim_token FROM idempotency_keys WHERE key = ?`, "cmd:1").Scan(&status, &token); err != nil {
		t.Fatal(err)
	}
	if status != "accepted" || token != "" {
		t.Fatalf("accepted row status=%q token=%q", status, token)
	}
	if duplicate, duplicateNew, duplicateErr := mgr.Begin(ctx, "cmd:1", time.Minute); duplicateErr != nil || duplicateNew || duplicate != nil {
		t.Fatalf("accepted duplicate claim: claim=%v isNew=%v err=%v", duplicate, duplicateNew, duplicateErr)
	}
}

func TestSQLiteExecutionClaimReleaseIsGenerationFenced(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db)
	ctx := context.Background()
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if claimed, err := repo.BeginClaim(ctx, "cmd:stale", "old-token", now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil || !claimed {
		t.Fatalf("seed expired generation: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := repo.BeginClaim(ctx, "cmd:stale", "new-token", now, now.Add(time.Hour)); err != nil || !claimed {
		t.Fatalf("reclaim generation: claimed=%v err=%v", claimed, err)
	}
	if released, err := repo.ReleaseClaim(ctx, "cmd:stale", "old-token"); err != nil || released {
		t.Fatalf("stale release: released=%v err=%v", released, err)
	}
	if processed, err := repo.IsProcessed(ctx, "cmd:stale", now); err != nil || !processed {
		t.Fatalf("new generation lost after stale release: processed=%v err=%v", processed, err)
	}
}

func TestSQLiteIdempotencyInitSchemaUpgradesLegacyLifecycleColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE idempotency_keys (
			key TEXT PRIMARY KEY,
			created_at_ms INTEGER NOT NULL,
			expires_at_ms INTEGER NOT NULL
		);
	`); err != nil {
		t.Fatal(err)
	}

	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"status", "claim_token"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(1) FROM pragma_table_info('idempotency_keys') WHERE name = ?", column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("column %q count=%d, want 1", column, count)
		}
	}
}
