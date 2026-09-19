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

func TestCommitAttemptDeferredAtomicReplayAndRedrive(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{
		ID: "occ-deferred", JobID: "job", OccurrenceKey: "deferred-key",
	}); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.PrepareAttemptLease(ctx, "occ-deferred", "task-deferred-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	readyAt := time.Now().UTC().Add(75 * time.Millisecond)
	const reason = "telegram flood wait"
	if err := s.CommitAttemptDeferred(ctx, attempt.ID, attempt.LeaseEpoch, readyAt, reason); err != nil {
		t.Fatalf("commit deferred attempt: %v", err)
	}

	latest, err := s.LatestAttempt(ctx, "occ-deferred")
	if err != nil {
		t.Fatal(err)
	}
	if latest.State != jobs.AttemptDeferred || latest.Error != reason || latest.FinishedAt.IsZero() {
		t.Fatalf("deferred attempt not persisted atomically: %+v", latest)
	}
	occ, err := s.GetOccurrence(ctx, "occ-deferred")
	if err != nil {
		t.Fatal(err)
	}
	if occ.State != jobs.OccurrenceDispatched || !occ.ReadyAt.Equal(readyAt) {
		t.Fatalf("deferred occurrence mismatch: %+v; ready_at want %v", occ, readyAt)
	}
	revision := occ.Revision

	// Lost acknowledgement replay must be a no-op, including occurrence revision.
	if err := s.CommitAttemptDeferred(ctx, attempt.ID, attempt.LeaseEpoch, readyAt, reason); err != nil {
		t.Fatalf("idempotent deferred replay: %v", err)
	}
	occ, err = s.GetOccurrence(ctx, "occ-deferred")
	if err != nil {
		t.Fatal(err)
	}
	if occ.Revision != revision {
		t.Fatalf("idempotent replay changed revision: got %d want %d", occ.Revision, revision)
	}

	if err := s.CommitAttemptDeferred(ctx, attempt.ID, attempt.LeaseEpoch, readyAt.Add(time.Second), reason); !errors.Is(err, ErrLeaseFencingLost) {
		t.Fatalf("conflicting deferred deadline must be fenced, got %v", err)
	}
	if err := s.CommitAttemptDeferred(ctx, attempt.ID, attempt.LeaseEpoch, readyAt, "different reason"); !errors.Is(err, ErrLeaseFencingLost) {
		t.Fatalf("conflicting deferred reason must be fenced, got %v", err)
	}
	if _, err := s.PrepareAttemptLease(ctx, "occ-deferred", "task-deferred-2", time.Minute); !errors.Is(err, ErrOccurrenceNotReady) {
		t.Fatalf("deferred occurrence redrove before ready_at: %v", err)
	}

	if wait := time.Until(readyAt); wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	next, err := s.PrepareAttemptLease(ctx, "occ-deferred", "task-deferred-2", time.Minute)
	if err != nil {
		t.Fatalf("prepare after deferred deadline: %v", err)
	}
	if next.AttemptNo != 2 || next.LeaseEpoch == attempt.LeaseEpoch {
		t.Fatalf("deferred redrive must mint next physical attempt: first=%+v next=%+v", attempt, next)
	}
}

func TestCommitAttemptDeferredHonorsCancellationAndLeaseFencing(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{
		ID: "occ-deferred-cancel", JobID: "job", OccurrenceKey: "deferred-cancel-key",
	}); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.PrepareAttemptLease(ctx, "occ-deferred-cancel", "task-deferred-cancel-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	readyAt := time.Now().UTC().Add(time.Minute)

	if err := s.CommitAttemptDeferred(ctx, attempt.ID, attempt.LeaseEpoch+1, readyAt, "stale epoch"); !errors.Is(err, ErrLeaseFencingLost) {
		t.Fatalf("stale deferred epoch must be fenced, got %v", err)
	}
	if err := s.CancelOccurrence(ctx, "occ-deferred-cancel", "operator cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAttemptDeferred(ctx, attempt.ID, attempt.LeaseEpoch, readyAt, "late flood wait"); !errors.Is(err, ErrLeaseFencingLost) {
		t.Fatalf("cancelled occurrence must win over deferred commit, got %v", err)
	}
	occ, err := s.GetOccurrence(ctx, "occ-deferred-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if occ.State != jobs.OccurrenceCancelled {
		t.Fatalf("deferred commit revived cancelled occurrence: %+v", occ)
	}
}
