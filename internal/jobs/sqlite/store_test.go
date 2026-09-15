package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := InitSchema(context.Background(), db); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return db
}

func TestStore_DefinitionLifecycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewStore(db)

	def := &jobs.JobDefinition{
		ID:          "job-backup",
		ScopeOwner:  "system",
		QuotaOwner:  "admin",
		HandlerType: "sys.backup",
		Version:     1,
		Pool:        "general",
		Class:       "maintenance",
		Timeout:     5 * time.Minute,
		Enabled:     true,
	}

	if err := store.SaveDefinition(context.Background(), def); err != nil {
		t.Fatalf("SaveDefinition failed: %v", err)
	}

	loaded, err := store.GetDefinition(context.Background(), "job-backup")
	if err != nil {
		t.Fatalf("GetDefinition failed: %v", err)
	}

	if loaded.HandlerType != "sys.backup" || loaded.Class != "maintenance" || !loaded.Enabled {
		t.Errorf("definition fields mismatch: %+v", loaded)
	}
}

func TestStore_OccurrenceAndAttemptProtocol(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewStore(db)

	def := &jobs.JobDefinition{
		ID:          "job-sync",
		ScopeOwner:  "plugin:sync",
		QuotaOwner:  "user-1",
		HandlerType: "sync.run",
		Enabled:     true,
	}
	_ = store.SaveDefinition(context.Background(), def)

	now := time.Now().UTC()
	occ := &jobs.JobOccurrence{
		ID:            "occ-100",
		JobID:         "job-sync",
		ScheduledFor:  now,
		OccurrenceKey: "job-sync:2026-09-15T00:00:00Z",
		State:         jobs.OccurrenceReady,
		ReadyAt:       now,
	}

	if err := store.MaterializeOccurrence(context.Background(), occ); err != nil {
		t.Fatalf("MaterializeOccurrence failed: %v", err)
	}

	// Ready occurrences should list occ-100
	readyList, err := store.ListReadyOccurrences(context.Background(), 10)
	if err != nil || len(readyList) != 1 || readyList[0].ID != "occ-100" {
		t.Fatalf("ListReadyOccurrences mismatch: %v (err: %v)", readyList, err)
	}

	// Prepare attempt lease
	attempt, err := store.PrepareAttemptLease(context.Background(), "occ-100", "task-abc", 30*time.Second)
	if err != nil {
		t.Fatalf("PrepareAttemptLease failed: %v", err)
	}
	if attempt.AttemptNo != 1 || attempt.State != jobs.AttemptLeased {
		t.Errorf("attempt mismatch: %+v", attempt)
	}

	// Materialized occurrence is now dispatched, ready list must be empty
	readyList, _ = store.ListReadyOccurrences(context.Background(), 10)
	if len(readyList) != 0 {
		t.Errorf("expected 0 ready occurrences after lease, got %d", len(readyList))
	}

	// Test CAS fencing: commit with wrong lease epoch must fail
	staleErr := store.CommitAttemptResult(context.Background(), attempt.ID, 999999, jobs.AttemptCompleted, []byte("ok"), "")
	if !errors.Is(staleErr, ErrLeaseFencingLost) {
		t.Errorf("expected ErrLeaseFencingLost for stale epoch, got: %v", staleErr)
	}

	// Commit with correct lease epoch must succeed
	if err := store.CommitAttemptResult(context.Background(), attempt.ID, attempt.LeaseEpoch, jobs.AttemptCompleted, []byte("ok"), ""); err != nil {
		t.Fatalf("CommitAttemptResult failed: %v", err)
	}
}

func TestStoreCompletionReplayCannotOverwriteResult(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()
	if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{ID: "occ", JobID: "job", OccurrenceKey: "key"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.PrepareAttemptLease(ctx, "occ", "task", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAttemptResult(ctx, a.ID, a.LeaseEpoch, jobs.AttemptCompleted, []byte("original"), ""); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := db.QueryRow(`SELECT finished_at FROM job_attempts WHERE id = ?`, a.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAttemptResult(ctx, a.ID, a.LeaseEpoch, jobs.AttemptCompleted, []byte("original"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAttemptResult(ctx, a.ID, a.LeaseEpoch, jobs.AttemptFailed, nil, "late failure"); !errors.Is(err, ErrLeaseFencingLost) {
		t.Fatal(err)
	}
	var after time.Time
	var state string
	if err := db.QueryRow(`SELECT finished_at, state FROM job_attempts WHERE id = ?`, a.ID).Scan(&after, &state); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) || state != string(jobs.AttemptCompleted) {
		t.Fatalf("replay changed result: %s %v %v", state, before, after)
	}
}

func TestStoreFutureReadyAndCancelledOutcome(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()
	if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{ID: "occ", JobID: "job", OccurrenceKey: "key", ReadyAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	ready, err := s.ListReadyOccurrences(ctx, 10)
	if err != nil || len(ready) != 0 {
		t.Fatalf("future occurrence exposed: %v %v", ready, err)
	}
	if _, err := s.PrepareAttemptLease(ctx, "occ", "early", time.Minute); !errors.Is(err, ErrOccurrenceNotReady) {
		t.Fatalf("future lease: %v", err)
	}
	if _, err := db.Exec(`UPDATE job_occurrences SET ready_at = ? WHERE id = 'occ'`, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	a, err := s.PrepareAttemptLease(ctx, "occ", "task", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAttemptResult(ctx, a.ID, a.LeaseEpoch, jobs.AttemptCancelled, nil, "cancelled"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM job_occurrences WHERE id = 'occ'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(jobs.OccurrenceCancelled) {
		t.Fatal(state)
	}
}
