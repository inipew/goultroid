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

func TestStoreScheduleLifecycleAndMaterializeDue(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	def := &jobs.JobDefinition{
		ID:          "job-sched-test",
		ScopeOwner:  "system",
		QuotaOwner:  "admin",
		HandlerType: "test.run",
		Enabled:     true,
	}
	if err := s.SaveDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}

	dueTime := time.Now().UTC().Add(-10 * time.Second)
	sched := &jobs.JobSchedule{
		ID:            "sched-1",
		JobID:         "job-sched-test",
		Recurrence:    "@hourly",
		Interval:      time.Hour,
		NextDueAt:     dueTime,
		MisfirePolicy: jobs.MisfireRunOnce,
		OverlapPolicy: jobs.OverlapForbid,
		Enabled:       true,
		Revision:      1,
	}
	if err := s.SaveSchedule(ctx, sched); err != nil {
		t.Fatalf("SaveSchedule failed: %v", err)
	}

	loadedSched, err := s.GetSchedule(ctx, "sched-1")
	if err != nil {
		t.Fatalf("GetSchedule failed: %v", err)
	}
	if loadedSched.ID != "sched-1" || loadedSched.Interval != time.Hour {
		t.Fatalf("loaded schedule mismatch: %+v", loadedSched)
	}

	// Materialize due schedule
	nextDue := time.Now().UTC().Add(time.Hour)
	occ, err := s.MaterializeDueSchedule(ctx, "sched-1", nextDue)
	if err != nil {
		t.Fatalf("MaterializeDueSchedule failed: %v", err)
	}
	if occ.JobID != "job-sched-test" || occ.ScheduleID != "sched-1" || occ.State != jobs.OccurrenceReady {
		t.Fatalf("materialized occurrence mismatch: %+v", occ)
	}

	// Schedule next_due_at should have been advanced
	updatedSched, err := s.GetSchedule(ctx, "sched-1")
	if err != nil {
		t.Fatal(err)
	}
	if !updatedSched.NextDueAt.Equal(nextDue.Truncate(time.Second)) && !updatedSched.NextDueAt.Equal(nextDue) {
		t.Fatalf("schedule next_due_at was not advanced: got %v, expected %v", updatedSched.NextDueAt, nextDue)
	}

	// Forbid overlap: when occurrence is still ready, materializing again does not duplicate occurrence
	occ2, err := s.MaterializeDueSchedule(ctx, "sched-1", nextDue.Add(time.Hour))
	if err == nil && occ2 != nil {
		t.Fatalf("expected overlap forbid to return nil occurrence, got %+v", occ2)
	}
}

func TestStoreCancelOccurrenceAndEpochFencing(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	def := &jobs.JobDefinition{
		ID:          "job-cancel-test",
		ScopeOwner:  "system",
		QuotaOwner:  "admin",
		HandlerType: "test.cancel",
		Enabled:     true,
	}
	_ = s.SaveDefinition(ctx, def)

	now := time.Now().UTC()
	occ := &jobs.JobOccurrence{
		ID:            "occ-cancel-1",
		JobID:         "job-cancel-test",
		ScheduledFor:  now,
		OccurrenceKey: "key-cancel-1",
		State:         jobs.OccurrenceReady,
		ReadyAt:       now,
	}
	if err := s.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}

	attempt, err := s.PrepareAttemptLease(ctx, "occ-cancel-1", "task-cancel-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Cancel the occurrence
	if err := s.CancelOccurrence(ctx, "occ-cancel-1", "user requested"); err != nil {
		t.Fatalf("CancelOccurrence failed: %v", err)
	}

	// Attempt commit should now be rejected due to cancel epoch bump and occurrence state != dispatched
	err = s.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, jobs.AttemptCompleted, []byte("res"), "")
	if !errors.Is(err, ErrLeaseFencingLost) {
		t.Fatalf("expected ErrLeaseFencingLost after cancel, got: %v", err)
	}

	// Outbox should contain cancellation event
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_outbox WHERE kind = 'occurrence_cancelled' AND occurrence_id = 'occ-cancel-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 cancellation outbox event, got %d", count)
	}
}

func TestStoreCommitAttemptResultWithOutbox(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	def := &jobs.JobDefinition{
		ID:          "job-outbox-test",
		ScopeOwner:  "system",
		QuotaOwner:  "admin",
		HandlerType: "test.outbox",
		Enabled:     true,
	}
	_ = s.SaveDefinition(ctx, def)

	now := time.Now().UTC()
	occ := &jobs.JobOccurrence{
		ID:            "occ-outbox-1",
		JobID:         "job-outbox-test",
		ScheduledFor:  now,
		OccurrenceKey: "key-outbox-1",
		State:         jobs.OccurrenceReady,
		ReadyAt:       now,
	}
	_ = s.MaterializeOccurrence(ctx, occ)

	attempt, err := s.PrepareAttemptLease(ctx, "occ-outbox-1", "task-outbox-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	err = s.CommitAttemptResultWithOutbox(ctx, attempt.ID, attempt.LeaseEpoch, jobs.AttemptCompleted, []byte("ok"), "", "event-1", "job_finished", []byte(`{"status":"success"}`))
	if err != nil {
		t.Fatalf("CommitAttemptResultWithOutbox failed: %v", err)
	}

	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_outbox WHERE event_id = 'event-1' AND kind = 'job_finished'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 outbox event, got %d", eventCount)
	}
}
