package jobs

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *SQLiteRepository {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite in-memory db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}
	return repo
}

func TestSQLiteRepository_SaveAndGet(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	job := &Job{
		ID:             "job-1",
		Owner:          "plugin:downloader",
		Type:           "download_cleanup",
		Schedule:       "@every 10m",
		Payload:        []byte("test-payload"),
		RecoveryPolicy: RecoveryRunImmediately,
		IdempotencyKey: "key-123",
		Timeout:        5 * time.Minute,
		Pool:           "download",
		NextRun:        now.Add(10 * time.Minute),
		LastRun:        now,
		State:          StateRegistered,
		LastError:      "",
	}

	if err := repo.Save(ctx, job); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	got, err := repo.Get(ctx, "job-1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if got.ID != job.ID {
		t.Errorf("ID mismatch: got %v, want %v", got.ID, job.ID)
	}
	if got.Owner != job.Owner {
		t.Errorf("Owner mismatch: got %v, want %v", got.Owner, job.Owner)
	}
	if got.Type != job.Type {
		t.Errorf("Type mismatch: got %v, want %v", got.Type, job.Type)
	}
	if got.Pool != "download" {
		t.Errorf("Pool mismatch: got %v, want download", got.Pool)
	}
	if got.RecoveryPolicy != RecoveryRunImmediately {
		t.Errorf("RecoveryPolicy mismatch: got %v, want %v", got.RecoveryPolicy, RecoveryRunImmediately)
	}
	if string(got.Payload) != "test-payload" {
		t.Errorf("Payload mismatch: got %v, want %v", string(got.Payload), "test-payload")
	}
	if got.Timeout != 5*time.Minute {
		t.Errorf("Timeout mismatch: got %v, want 5m", got.Timeout)
	}
	if got.State != StateRegistered {
		t.Errorf("State mismatch: got %v, want %v", got.State, StateRegistered)
	}
}

func TestSQLiteRepository_UpdateStateAndListActive(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	job1 := &Job{
		ID:             "j1",
		Owner:          "media",
		Type:           "cleanup",
		Schedule:       "@daily",
		RecoveryPolicy: RecoverySkip,
		State:          StateRegistered,
	}
	job2 := &Job{
		ID:             "j2",
		Owner:          "media",
		Type:           "cleanup",
		Schedule:       "@hourly",
		RecoveryPolicy: RecoveryRunImmediately,
		State:          StateRegistered,
	}

	_ = repo.Save(ctx, job1)
	_ = repo.Save(ctx, job2)

	now := time.Now().UTC().Truncate(time.Millisecond)
	next := now.Add(time.Hour)
	if err := repo.UpdateState(ctx, "j1", StateCompleted, "", now, next); err != nil {
		t.Fatalf("UpdateState failed: %v", err)
	}

	active, err := repo.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive failed: %v", err)
	}
	if len(active) != 1 || active[0].ID != "j2" {
		t.Fatalf("expected only j2 in active jobs, got: %v", active)
	}

	byOwner, err := repo.ListByOwner(ctx, "media")
	if err != nil {
		t.Fatalf("ListByOwner failed: %v", err)
	}
	if len(byOwner) != 2 {
		t.Errorf("expected 2 jobs for owner media, got %d", len(byOwner))
	}

	deleted, err := repo.DeleteByOwner(ctx, "media")
	if err != nil {
		t.Fatalf("DeleteByOwner failed: %v", err)
	}
	if deleted != 2 {
		t.Errorf("expected 2 deleted rows, got %d", deleted)
	}
}
