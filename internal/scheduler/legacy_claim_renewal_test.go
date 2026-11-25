package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
	"go.uber.org/zap"
)

type memoryOccurrenceStore struct {
	executionReconcileOccurrenceStore
	occurrences map[string]*jobs.JobOccurrence
	blocked     map[string]bool
}

func newMemoryOccurrenceStore() *memoryOccurrenceStore {
	return &memoryOccurrenceStore{
		occurrences: make(map[string]*jobs.JobOccurrence),
		blocked:     make(map[string]bool),
	}
}

func (s *memoryOccurrenceStore) GetOccurrence(_ context.Context, id string) (*jobs.JobOccurrence, error) {
	occ := s.occurrences[id]
	if occ == nil {
		return nil, jobs.ErrOccurrenceNotFound
	}
	copy := *occ
	return &copy, nil
}

func (s *memoryOccurrenceStore) WasBlockedOccurrence(_ context.Context, id string) (bool, error) {
	return s.blocked[id], nil
}

func TestLegacyClaimLeaseRenewedWhileAttemptRunning(t *testing.T) {
	ctx := context.Background()
	repo, _ := setupTestSchedulerRepo(t)

	now := time.Now().UTC()
	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          1001,
		PeerType:        "user",
		ActionType:      "message",
		Payload:         "renew test",
		IntervalSeconds: 3600,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 90*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim failed: %v", err)
	}
	claimToken := claimed[0].ClaimToken

	// Simulate lease nearing expiry (e.g. only 10s left, within schedulerClaimLease/2 window)
	nearExpiry := now.Add(10 * time.Second)
	if err := repo.RenewJobLease(ctx, job.ID, claimToken, 10*time.Second, now); err != nil {
		t.Fatal(err)
	}

	occStore := newMemoryOccurrenceStore()
	occID := "occ:test:running"
	occStore.occurrences[occID] = &jobs.JobOccurrence{
		ID:    occID,
		JobID: "job.def.1",
		State: jobs.OccurrenceDispatched,
	}

	jobsMgr := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Occurrences: occStore,
	}, nil)

	engine := NewEngine(repo, zap.NewNop())
	engine.SetJobsManager(jobsMgr)
	engine.trackClaim(job.ID, claimToken, "job.def.1", occID)

	// Run reconciliation pass
	engine.reconcileSettledClaims(ctx)

	diag := engine.Diagnostics()
	if diag.ClaimLeaseRenews != 1 {
		t.Fatalf("expected 1 claim lease renewal, got %d", diag.ClaimLeaseRenews)
	}
	if diag.ActiveLegacyClaims != 1 {
		t.Fatalf("expected claim to remain actively tracked, got %d", diag.ActiveLegacyClaims)
	}

	updated, err := repo.GetScheduledJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LeaseUntil == nil {
		t.Fatal("expected lease_until to be set")
	}
	// Lease should be extended to approximately now + schedulerClaimLease (3 minutes)
	expectedMinLease := nearExpiry.Add(time.Minute)
	if updated.LeaseUntil.Before(expectedMinLease) {
		t.Fatalf("lease_until %v was not extended past %v", updated.LeaseUntil, expectedMinLease)
	}
	if updated.ClaimToken != claimToken {
		t.Fatalf("claimToken changed: got %s, want %s", updated.ClaimToken, claimToken)
	}
}

func TestLegacyClaimRenewalStopsAfterOccurrenceSettles(t *testing.T) {
	ctx := context.Background()
	repo, _ := setupTestSchedulerRepo(t)

	now := time.Now().UTC()
	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          1002,
		PeerType:        "user",
		ActionType:      "message",
		Payload:         "settle test",
		IntervalSeconds: 3600,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 90*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim failed: %v", err)
	}
	claimToken := claimed[0].ClaimToken

	occStore := newMemoryOccurrenceStore()
	occID := "occ:test:settled"
	occStore.occurrences[occID] = &jobs.JobOccurrence{
		ID:    occID,
		JobID: "job.def.1",
		State: jobs.OccurrenceCompleted,
	}

	jobsMgr := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Occurrences: occStore,
	}, nil)

	engine := NewEngine(repo, zap.NewNop())
	engine.SetJobsManager(jobsMgr)
	engine.trackClaim(job.ID, claimToken, "job.def.1", occID)

	// Run reconciliation pass
	engine.reconcileSettledClaims(ctx)

	diag := engine.Diagnostics()
	if diag.ClaimLeaseRenews != 0 {
		t.Fatalf("settled occurrence should not renew lease, got %d renews", diag.ClaimLeaseRenews)
	}
	if diag.ActiveLegacyClaims != 0 {
		t.Fatalf("expected claim to be untracked after settlement, got %d", diag.ActiveLegacyClaims)
	}

	// Job should now be pending (advanced to next run)
	updated, err := repo.GetScheduledJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != JobStatusPending {
		t.Fatalf("job status = %s, want pending", updated.Status)
	}

	// Subsequent reconciliation pass does nothing
	engine.reconcileSettledClaims(ctx)
	if engine.Diagnostics().ClaimLeaseRenews != 0 {
		t.Fatal("subsequent reconciliation unexpectedly renewed lease")
	}
}

func TestLegacyClaimRenewalStopsAfterClaimTokenChanges(t *testing.T) {
	ctx := context.Background()
	repo, _ := setupTestSchedulerRepo(t)

	now := time.Now().UTC()
	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          1003,
		PeerType:        "user",
		ActionType:      "message",
		Payload:         "token change test",
		IntervalSeconds: 3600,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 90*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim failed: %v", err)
	}
	oldToken := claimed[0].ClaimToken

	// Engine tracks old token
	occStore := newMemoryOccurrenceStore()
	occID := "occ:test:token-change"
	occStore.occurrences[occID] = &jobs.JobOccurrence{
		ID:    occID,
		JobID: "job.def.1",
		State: jobs.OccurrenceDispatched,
	}

	jobsMgr := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Occurrences: occStore,
	}, nil)

	engine := NewEngine(repo, zap.NewNop())
	engine.SetJobsManager(jobsMgr)
	engine.trackClaim(job.ID, oldToken, "job.def.1", occID)

	// Simulate another worker reclaiming the job with a new token
	newToken := "claim-reclaimed-by-worker-2"
	_, err = repo.db.ExecContext(ctx, "UPDATE scheduled_jobs SET claim_token = ? WHERE id = ?", newToken, job.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Run reconciliation pass
	engine.reconcileSettledClaims(ctx)

	diag := engine.Diagnostics()
	if diag.ClaimLeaseRenews != 0 {
		t.Fatalf("should not renew when token mismatched, got %d renews", diag.ClaimLeaseRenews)
	}
	if diag.ActiveLegacyClaims != 0 {
		t.Fatalf("expected claim to be untracked after token mismatch, got %d", diag.ActiveLegacyClaims)
	}

	// Ensure database still has the new worker's token
	updated, err := repo.GetScheduledJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ClaimToken != newToken {
		t.Fatalf("claim token overwritten: got %s, want %s", updated.ClaimToken, newToken)
	}
}

func TestLegacyClaimRenewalDoesNotRenewBlockedOccurrence(t *testing.T) {
	ctx := context.Background()
	repo, _ := setupTestSchedulerRepo(t)

	now := time.Now().UTC()
	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          1004,
		PeerType:        "user",
		ActionType:      "message",
		Payload:         "blocked test",
		IntervalSeconds: 3600,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 90*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim failed: %v", err)
	}
	claimToken := claimed[0].ClaimToken

	// Set lease near expiry so it would renew if not blocked
	if err := repo.RenewJobLease(ctx, job.ID, claimToken, 10*time.Second, now); err != nil {
		t.Fatal(err)
	}

	occStore := newMemoryOccurrenceStore()
	occID := "occ:test:blocked"
	occStore.occurrences[occID] = &jobs.JobOccurrence{
		ID:    occID,
		JobID: "job.def.1",
		State: jobs.OccurrenceBlocked,
	}

	jobsMgr := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Occurrences: occStore,
	}, nil)

	engine := NewEngine(repo, zap.NewNop())
	engine.SetJobsManager(jobsMgr)
	engine.trackClaim(job.ID, claimToken, "job.def.1", occID)

	// Run reconciliation pass
	engine.reconcileSettledClaims(ctx)

	diag := engine.Diagnostics()
	if diag.ClaimLeaseRenews != 0 {
		t.Fatalf("blocked occurrence must not renew lease, got %d renews", diag.ClaimLeaseRenews)
	}
	if diag.ActiveLegacyClaims != 0 {
		t.Fatalf("expected blocked claim to be untracked, got %d", diag.ActiveLegacyClaims)
	}

	// Job should be marked failed due to quarantine
	updated, err := repo.GetScheduledJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != JobStatusFailed {
		t.Fatalf("job status = %s, want failed", updated.Status)
	}
	if updated.LastError != blockedOccurrenceReason {
		t.Fatalf("job last_error = %s, want %s", updated.LastError, blockedOccurrenceReason)
	}
}

func TestLegacyClaimCanBeReclaimedAfterOwnerStopsRenewing(t *testing.T) {
	ctx := context.Background()
	repo, _ := setupTestSchedulerRepo(t)

	now := time.Now().UTC()
	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          1005,
		PeerType:        "user",
		ActionType:      "message",
		Payload:         "reclaim after expiry",
		IntervalSeconds: 3600,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}

	claimed1, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 50*time.Millisecond)
	if err != nil || len(claimed1) != 1 {
		t.Fatalf("claim 1 failed: %v", err)
	}
	token1 := claimed1[0].ClaimToken

	// Fast-forward time past lease expiration
	tLater := now.Add(200 * time.Millisecond)

	// Second worker claims the expired job
	claimed2, err := repo.ClaimDueScheduledJobs(ctx, tLater, 1, 90*time.Second)
	if err != nil || len(claimed2) != 1 {
		t.Fatalf("claim 2 failed: %v (len %d)", err, len(claimed2))
	}
	token2 := claimed2[0].ClaimToken
	if token2 == token1 {
		t.Fatalf("reclaimed job must receive a new claim token: token1=%s, token2=%s", token1, token2)
	}

	// Stale owner attempts to renew lease -> must fail with ErrJobLeaseLost
	err = repo.RenewJobLease(ctx, job.ID, token1, 90*time.Second, tLater)
	if err != ErrJobLeaseLost {
		t.Fatalf("stale renew error = %v, want ErrJobLeaseLost", err)
	}

	// Stale owner attempts to complete job -> must fail with ErrJobLeaseLost
	err = repo.CompleteScheduledJob(ctx, job.ID, token1, 100, tLater)
	if err != ErrJobLeaseLost {
		t.Fatalf("stale complete error = %v, want ErrJobLeaseLost", err)
	}

	// Valid owner can complete the job
	err = repo.CompleteScheduledJob(ctx, job.ID, token2, 100, tLater)
	if err != nil {
		t.Fatalf("valid complete error = %v", err)
	}
}

func TestMultiEngineStaleOwnerCannotRenewOrSettleReclaimedClaim(t *testing.T) {
	ctx := context.Background()
	repo, db := setupTestSchedulerRepo(t)

	if err := jobsqlite.InitSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	jobStore := jobsqlite.NewStore(db)

	now := time.Now().UTC()
	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          1006,
		PeerType:        "user",
		ActionType:      "message",
		Payload:         "multi engine fencing",
		IntervalSeconds: 3600,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Engine 1 claims the job
	claimed1, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 50*time.Millisecond)
	if err != nil || len(claimed1) != 1 {
		t.Fatalf("claim 1 failed: %v", err)
	}
	token1 := claimed1[0].ClaimToken

	occ1 := &jobs.JobOccurrence{
		ID:            "occ:multi:1",
		JobID:         "job.multi",
		OccurrenceKey: "key:multi:1",
		State:         jobs.OccurrenceDispatched,
		ReadyAt:       now,
	}
	if err := jobStore.MaterializeOccurrence(ctx, occ1); err != nil {
		t.Fatal(err)
	}

	jobsMgr1 := jobs.NewManagerWithPorts(nil, jobs.StorePorts{
		Occurrences: jobStore,
		Recovery:    jobStore,
	}, nil)

	engine1 := NewEngine(repo, zap.NewNop())
	engine1.SetJobsManager(jobsMgr1)
	engine1.trackClaim(job.ID, token1, "job.multi", occ1.ID)

	// Time advances, lease expires
	reclaimTime := now.Add(200 * time.Millisecond)

	// Engine 2 claims the expired job
	claimed2, err := repo.ClaimDueScheduledJobs(ctx, reclaimTime, 1, 90*time.Second)
	if err != nil || len(claimed2) != 1 {
		t.Fatalf("claim 2 failed: %v", err)
	}
	token2 := claimed2[0].ClaimToken

	// Engine 1 runs reconciliation for its tracked claim
	engine1.reconcileSettledClaims(ctx)

	// Engine 1 must have detected lease loss and untracked the claim
	if engine1.Diagnostics().ActiveLegacyClaims != 0 {
		t.Fatalf("engine 1 active claims = %d, want 0 after fencing loss", engine1.Diagnostics().ActiveLegacyClaims)
	}

	// Engine 2's claim must be intact in the database
	currentJob, err := repo.GetScheduledJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentJob.ClaimToken != token2 {
		t.Fatalf("current claim token = %s, want engine 2's token %s", currentJob.ClaimToken, token2)
	}
	if currentJob.Status != JobStatusRunning {
		t.Fatalf("current job status = %s, want running", currentJob.Status)
	}
}
