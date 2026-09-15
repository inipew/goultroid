package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

// D1: definition updates are compare-and-swap on revision.
func TestUpdateDefinitionCASConflict(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()
	def := &jobs.JobDefinition{ID: "job-cas", ScopeOwner: "system", QuotaOwner: "admin", HandlerType: "h", Enabled: true}
	if err := s.SaveDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetDefinition(ctx, "job-cas")
	if err != nil {
		t.Fatal(err)
	}
	// Correct revision wins and bumps.
	loaded.Pool = "general"
	if err := s.UpdateDefinitionCAS(ctx, loaded, loaded.Revision); err != nil {
		t.Fatalf("CAS with current revision: %v", err)
	}
	if loaded.Revision == 0 {
		t.Fatalf("expected bumped revision")
	}
	// Stale revision loses without overwriting.
	stale := &jobs.JobDefinition{ID: "job-cas", ScopeOwner: "system", QuotaOwner: "admin", HandlerType: "h", Enabled: true, Pool: "other"}
	if err := s.UpdateDefinitionCAS(ctx, stale, loaded.Revision-1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict, got %v", err)
	}
	current, err := s.GetDefinition(ctx, "job-cas")
	if err != nil {
		t.Fatal(err)
	}
	if current.Pool != "general" {
		t.Fatalf("stale CAS overwrote definition: %+v", current)
	}
	if err := s.UpdateDefinitionCAS(ctx, stale, 9999); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict for unknown revision, got %v", err)
	}
	// Unknown definition surfaces not-found.
	ghost := &jobs.JobDefinition{ID: "nope", ScopeOwner: "s", QuotaOwner: "q", HandlerType: "h"}
	if err := s.UpdateDefinitionCAS(ctx, ghost, 1); !errors.Is(err, ErrDefinitionNotFound) {
		t.Fatalf("expected ErrDefinitionNotFound, got %v", err)
	}
}

// D1: materialization is idempotent on occurrence_key and resolves identity.
func TestMaterializeIdempotentIdentity(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()
	first := &jobs.JobOccurrence{ID: "occ-a", JobID: "job", OccurrenceKey: "dup-key", State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC()}
	if err := s.MaterializeOccurrence(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &jobs.JobOccurrence{ID: "occ-b", JobID: "job", OccurrenceKey: "dup-key", State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC()}
	if err := s.MaterializeOccurrence(ctx, second); err != nil {
		t.Fatalf("duplicate key must resolve identity, got %v", err)
	}
	if second.ID != "occ-a" {
		t.Fatalf("duplicate resolved to %q, want canonical occ-a", second.ID)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_occurrences WHERE occurrence_key = 'dup-key'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate logical run materialized %d rows", count)
	}
}

// D2: failed attempts leave the occurrence dispatched for retry; the next
// prepare mints a new fenced epoch; completion closes the occurrence.
func TestPrepareRetryAfterFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()
	if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{ID: "occ", JobID: "job", OccurrenceKey: "key"}); err != nil {
		t.Fatal(err)
	}
	a1, err := s.PrepareAttemptLease(ctx, "occ", "task-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAttemptResult(ctx, a1.ID, a1.LeaseEpoch, jobs.AttemptFailed, nil, "boom"); err != nil {
		t.Fatal(err)
	}
	var occState string
	if err := db.QueryRow(`SELECT state FROM job_occurrences WHERE id = 'occ'`).Scan(&occState); err != nil {
		t.Fatal(err)
	}
	if occState != string(jobs.OccurrenceDispatched) {
		t.Fatalf("failed attempt must leave occurrence dispatched, got %s", occState)
	}
	a2, err := s.PrepareAttemptLease(ctx, "occ", "task-2", time.Minute)
	if err != nil {
		t.Fatalf("retry prepare: %v", err)
	}
	if a2.AttemptNo != 2 || a2.LeaseEpoch == a1.LeaseEpoch {
		t.Fatalf("retry must mint a new fenced epoch: %+v vs %+v", a1, a2)
	}
	// The superseded attempt can never commit again.
	if err := s.CommitAttemptResult(ctx, a1.ID, a1.LeaseEpoch, jobs.AttemptCompleted, nil, ""); !errors.Is(err, ErrLeaseFencingLost) {
		t.Fatalf("expected fencing loss for superseded attempt, got %v", err)
	}
	if err := s.CommitAttemptResult(ctx, a2.ID, a2.LeaseEpoch, jobs.AttemptCompleted, []byte("ok"), ""); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM job_occurrences WHERE id = 'occ'`).Scan(&occState); err != nil {
		t.Fatal(err)
	}
	if occState != string(jobs.OccurrenceCompleted) {
		t.Fatalf("occurrence=%s, want completed", occState)
	}
}

// D2: a second prepare while an attempt is still live is rejected.
func TestPrepareRefusesLiveAttempt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()
	if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{ID: "occ", JobID: "job", OccurrenceKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareAttemptLease(ctx, "occ", "task-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareAttemptLease(ctx, "occ", "task-2", time.Minute); !errors.Is(err, ErrOccurrenceNotReady) {
		t.Fatalf("expected ErrOccurrenceNotReady for live attempt, got %v", err)
	}
}

// D2: explicit finalization closes exhausted occurrences idempotently.
func TestFinalizeOccurrence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()
	if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{ID: "occ", JobID: "job", OccurrenceKey: "key"}); err != nil {
		t.Fatal(err)
	}
	a1, err := s.PrepareAttemptLease(ctx, "occ", "task-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAttemptResult(ctx, a1.ID, a1.LeaseEpoch, jobs.AttemptTimedOut, nil, "slow"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeOccurrence(ctx, "occ", jobs.OccurrenceFailed); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Idempotent replay with the same state.
	if err := s.FinalizeOccurrence(ctx, "occ", jobs.OccurrenceFailed); err != nil {
		t.Fatalf("idempotent finalize: %v", err)
	}
	// Conflicting transition from terminal is rejected.
	if err := s.FinalizeOccurrence(ctx, "occ", jobs.OccurrenceCancelled); err == nil {
		t.Fatalf("expected rejection of conflicting finalize")
	}
	if err := s.FinalizeOccurrence(ctx, "occ", jobs.OccurrenceCompleted); err == nil {
		t.Fatalf("expected rejection of completed finalize")
	}
	if err := s.FinalizeOccurrence(ctx, "missing", jobs.OccurrenceFailed); !errors.Is(err, ErrOccurrenceNotFound) {
		t.Fatalf("expected ErrOccurrenceNotFound, got %v", err)
	}
	if _, err := s.PrepareAttemptLease(ctx, "occ", "task-2", time.Minute); !errors.Is(err, ErrOccurrenceNotReady) {
		t.Fatalf("finalized occurrence must refuse leases, got %v", err)
	}
}
