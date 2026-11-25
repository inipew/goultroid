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

func TestDurableDiagnosticsReportsDeferredState(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	firstReady := now.Add(2 * time.Minute)
	secondReady := now.Add(3 * time.Minute)

	for _, item := range []struct {
		occurrenceID string
		taskID       string
		key          string
		readyAt      time.Time
	}{
		{occurrenceID: "occ-diag-1", taskID: "task-diag-1", key: "diag-key-1", readyAt: firstReady},
		{occurrenceID: "occ-diag-2", taskID: "task-diag-2", key: "diag-key-2", readyAt: secondReady},
	} {
		if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{
			ID: item.occurrenceID, JobID: "job", OccurrenceKey: item.key,
		}); err != nil {
			t.Fatal(err)
		}
		attempt, err := s.PrepareAttemptLease(ctx, item.occurrenceID, item.taskID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CommitAttemptDeferred(ctx, attempt.ID, attempt.LeaseEpoch, item.readyAt, "rate limited"); err != nil {
			t.Fatal(err)
		}
	}

	snapshot, err := s.DurableDiagnostics(ctx, now)
	if err != nil {
		t.Fatalf("DurableDiagnostics() error = %v", err)
	}
	if snapshot.DeferredOccurrences != 2 {
		t.Fatalf("deferred occurrences=%d, want 2", snapshot.DeferredOccurrences)
	}
	if snapshot.RetainedDeferrals != 2 {
		t.Fatalf("retained deferrals=%d, want 2", snapshot.RetainedDeferrals)
	}
	if !snapshot.EarliestDeferredAt.Equal(firstReady) {
		t.Fatalf("earliest deferred=%v, want %v", snapshot.EarliestDeferredAt, firstReady)
	}

	manager := jobs.NewManagerWithPorts(nil, Ports(s), nil)
	diagnostics := manager.Diagnostics()
	if !diagnostics.DurableSnapshotOK ||
		diagnostics.DeferredOccurrences != 2 ||
		diagnostics.RetainedDeferrals != 2 ||
		!diagnostics.EarliestDeferredAt.Equal(firstReady) {
		t.Fatalf("manager diagnostics mismatch: %+v", diagnostics)
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

func TestDurableDiagnostics_ReadyOccurrences(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	// 1. Nilai nol ketika tidak ada ready occurrence
	snapEmpty, err := s.DurableDiagnostics(ctx, now)
	if err != nil {
		t.Fatalf("empty DB DurableDiagnostics error: %v", err)
	}
	if snapEmpty.ReadyOccurrences != 0 || snapEmpty.DeferredOccurrences != 0 || snapEmpty.BlockedOccurrences != 0 {
		t.Fatalf("expected all zeros, got %+v", snapEmpty)
	}

	// Helper to insert an occurrence with specified state and ready_at
	insertOcc := func(id, key, state string, readyAt time.Time) {
		t.Helper()
		_, err := db.ExecContext(ctx, `
			INSERT INTO job_occurrences (id, job_id, scheduled_for, occurrence_key, state, ready_at, cancel_epoch, revision, updated_at)
			VALUES (?, 'job', ?, ?, ?, ?, 0, 1, ?)
		`, id, now, key, state, readyAt, now)
		if err != nil {
			t.Fatalf("insert occurrence %s: %v", id, err)
		}
	}

	// 2. Dispatched occurrence with ready_at <= now (ready occurrence yang sudah jatuh tempo)
	dueReadyAt := now.Add(-10 * time.Second)
	insertOcc("occ-ready-due-1", "key-ready-due-1", "dispatched", dueReadyAt)
	insertOcc("occ-ready-due-2", "key-ready-due-2", "dispatched", now)

	snap1, err := s.DurableDiagnostics(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if snap1.ReadyOccurrences != 2 {
		t.Fatalf("ReadyOccurrences = %d, want 2", snap1.ReadyOccurrences)
	}

	// 3. Dispatched occurrence dengan ready_at di masa depan (future ready_at, deferred)
	futureReadyAt := now.Add(5 * time.Minute)
	insertOcc("occ-dispatched-future", "key-dispatched-future", "dispatched", futureReadyAt)

	snap2, err := s.DurableDiagnostics(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if snap2.ReadyOccurrences != 2 {
		t.Fatalf("ReadyOccurrences should still be 2, got %d", snap2.ReadyOccurrences)
	}
	if snap2.DeferredOccurrences != 1 {
		t.Fatalf("DeferredOccurrences = %d, want 1", snap2.DeferredOccurrences)
	}

	// 4. Deferred occurrence (via CommitAttemptDeferred)
	if err := s.MaterializeOccurrence(ctx, &jobs.JobOccurrence{
		ID: "occ-deferred-attempt", JobID: "job", OccurrenceKey: "key-deferred-attempt",
	}); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.PrepareAttemptLease(ctx, "occ-deferred-attempt", "task-def-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAttemptDeferred(ctx, attempt.ID, attempt.LeaseEpoch, now.Add(10*time.Minute), "flood wait"); err != nil {
		t.Fatal(err)
	}

	snap3, err := s.DurableDiagnostics(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if snap3.ReadyOccurrences != 2 {
		t.Fatalf("ReadyOccurrences should still be 2, got %d", snap3.ReadyOccurrences)
	}
	if snap3.DeferredOccurrences != 2 {
		t.Fatalf("DeferredOccurrences = %d, want 2", snap3.DeferredOccurrences)
	}
	if snap3.RetainedDeferrals != 1 {
		t.Fatalf("RetainedDeferrals = %d, want 1", snap3.RetainedDeferrals)
	}

	// 5. Blocked occurrence (ready_at in past, but state is blocked)
	insertOcc("occ-blocked", "key-blocked", "blocked", dueReadyAt)

	snap4, err := s.DurableDiagnostics(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if snap4.ReadyOccurrences != 2 {
		t.Fatalf("ReadyOccurrences should still be 2, got %d", snap4.ReadyOccurrences)
	}
	if snap4.BlockedOccurrences != 1 {
		t.Fatalf("BlockedOccurrences = %d, want 1", snap4.BlockedOccurrences)
	}

	// 6. Terminal occurrences (completed, failed, cancelled) with past ready_at
	insertOcc("occ-completed", "key-completed", "completed", dueReadyAt)
	insertOcc("occ-failed", "key-failed", "failed", dueReadyAt)
	insertOcc("occ-cancelled", "key-cancelled", "cancelled", dueReadyAt)

	// Occurrence with state = 'ready' (not yet dispatched) with past ready_at
	insertOcc("occ-initial-ready", "key-initial-ready", "ready", dueReadyAt)

	snap5, err := s.DurableDiagnostics(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if snap5.ReadyOccurrences != 2 {
		t.Fatalf("terminal and non-dispatched occurrences must not be counted in ReadyOccurrences, got %d, want 2", snap5.ReadyOccurrences)
	}

	// 7. Verify via Manager.Diagnostics()
	manager := jobs.NewManagerWithPorts(nil, Ports(s), nil)
	mgrDiag := manager.Diagnostics()
	if !mgrDiag.DurableSnapshotOK {
		t.Fatal("expected DurableSnapshotOK to be true")
	}
	if mgrDiag.ReadyOccurrences != 2 {
		t.Fatalf("manager Diagnostics ReadyOccurrences = %d, want 2", mgrDiag.ReadyOccurrences)
	}
	if mgrDiag.DeferredOccurrences != 2 {
		t.Fatalf("manager Diagnostics DeferredOccurrences = %d, want 2", mgrDiag.DeferredOccurrences)
	}
	if mgrDiag.BlockedOccurrences != 1 {
		t.Fatalf("manager Diagnostics BlockedOccurrences = %d, want 1", mgrDiag.BlockedOccurrences)
	}

	// 8. Context cancellation/timeout
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.DurableDiagnostics(cancelCtx, now)
	if err == nil {
		t.Fatal("expected DurableDiagnostics with cancelled context to fail")
	}
}

func TestDurableDiagnostics_ContextDeadlineRespected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	s := NewStore(db)
	now := time.Now().UTC()

	// 1. Expired deadline must fail immediately with context.DeadlineExceeded
	expiredCtx, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()

	_, err := s.DurableDiagnostics(expiredCtx, now)
	if err == nil {
		t.Fatal("expected DurableDiagnostics with expired deadline to fail")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}

	// 2. Pre-cancelled context must fail immediately with context.Canceled
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = s.DurableDiagnostics(canceledCtx, now)
	if err == nil {
		t.Fatal("expected DurableDiagnostics with cancelled context to fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
