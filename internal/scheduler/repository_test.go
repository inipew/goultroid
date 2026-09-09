package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestSchedulerRepo(t *testing.T) (*SQLiteRepository, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	repo := NewSQLiteRepository(db)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}
	return repo, db
}

func TestScheduledJobOperations(t *testing.T) {
	repo, _ := setupTestSchedulerRepo(t)
	ctx := context.Background()
	chatID := int64(998877)

	// 1. Initial list empty
	jobs, err := repo.ListScheduledJobs(ctx, chatID)
	if err != nil {
		t.Fatalf("unexpected error listing jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs, got %d", len(jobs))
	}

	// 2. Create one-shot job
	now := time.Now().Truncate(time.Second)
	job1 := &ScheduledJob{
		ChatID:          chatID,
		PeerType:        "chat",
		AccessHash:      0,
		ActionType:      "message",
		Payload:         "Don't forget medicine!",
		IntervalSeconds: 0,
		NextRunAt:       now.Add(10 * time.Minute),
	}
	created1, err := repo.CreateScheduledJob(ctx, job1)
	if err != nil {
		t.Fatalf("failed to create job1: %v", err)
	}
	if created1.ID == 0 {
		t.Fatalf("expected non-zero ID for created job1")
	}

	// 3. Create recurring job
	job2 := &ScheduledJob{
		ChatID:          chatID,
		PeerType:        "channel",
		AccessHash:      12345678,
		ActionType:      "command",
		Payload:         ".alive",
		IntervalSeconds: 3600,
		NextRunAt:       now.Add(1 * time.Hour),
	}
	created2, err := repo.CreateScheduledJob(ctx, job2)
	if err != nil {
		t.Fatalf("failed to create job2: %v", err)
	}

	// 4. Get job
	fetched, err := repo.GetScheduledJob(ctx, created1.ID)
	if err != nil {
		t.Fatalf("failed to get job1: %v", err)
	}
	if fetched == nil || fetched.Payload != "Don't forget medicine!" || fetched.PeerType != "chat" {
		t.Fatalf("unexpected fetched job: %+v", fetched)
	}

	fetched2, err := repo.GetScheduledJob(ctx, created2.ID)
	if err != nil || fetched2.AccessHash != 12345678 || fetched2.PeerType != "channel" {
		t.Fatalf("unexpected fetched job2: %+v", fetched2)
	}

	// 5. List jobs by chat
	all, err := repo.ListScheduledJobs(ctx, chatID)
	if err != nil {
		t.Fatalf("failed to list jobs: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(all))
	}
	if all[0].ID != created1.ID || all[1].ID != created2.ID {
		t.Errorf("jobs not in expected order: %+v", all)
	}

	// 6. List due jobs
	due, err := repo.ListDueScheduledJobs(ctx, now.Add(15*time.Minute))
	if err != nil {
		t.Fatalf("failed to list due jobs: %v", err)
	}
	if len(due) != 1 || due[0].ID != created1.ID {
		t.Fatalf("expected 1 due job (created1), got %d: %+v", len(due), due)
	}

	// 7. Update next run
	newNextRun := now.Add(2 * time.Hour)
	if err := repo.UpdateScheduledJobNextRun(ctx, created2.ID, newNextRun); err != nil {
		t.Fatalf("failed to update next run: %v", err)
	}
	updated, _ := repo.GetScheduledJob(ctx, created2.ID)
	if !updated.NextRunAt.Equal(newNextRun) {
		t.Errorf("expected NextRunAt %v, got %v", newNextRun, updated.NextRunAt)
	}

	// 8. Delete job
	if err := repo.DeleteScheduledJob(ctx, created1.ID); err != nil {
		t.Fatalf("failed to delete job1: %v", err)
	}
	allAfter, _ := repo.ListScheduledJobs(ctx, chatID)
	if len(allAfter) != 1 || allAfter[0].ID != created2.ID {
		t.Errorf("expected only created2 remaining, got %d", len(allAfter))
	}

	// 9. Delete non-existent job
	if err := repo.DeleteScheduledJob(ctx, 999999); err == nil {
		t.Errorf("expected error deleting non-existent job")
	}
}

func TestScheduledJob_ClaimCompleteAndFail(t *testing.T) {
	repo, _ := setupTestSchedulerRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Create job due in past
	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          123,
		PeerType:        "chat",
		ActionType:      "message",
		Payload:         "test-payload",
		IntervalSeconds: 60,
		NextRunAt:       now.Add(-time.Minute),
		MaxAttempts:     3,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Claim it
	claimed, err := repo.ClaimDueScheduledJobs(ctx, now, 10, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected 1 claimed job, got %d", len(claimed))
	}
	token := claimed[0].ClaimToken
	if token == "" {
		t.Fatal("expected claim token")
	}

	// Renew lease
	if err := repo.RenewJobLease(ctx, job.ID, token, 60*time.Second, now); err != nil {
		t.Fatalf("failed to renew lease: %v", err)
	}

	// Complete job (recurring)
	if err := repo.CompleteScheduledJob(ctx, job.ID, token, 150, now); err != nil {
		t.Fatalf("failed to complete job: %v", err)
	}

	// Check history
	hist, err := repo.GetJobHistory(ctx, job.ID, 10)
	if err != nil {
		t.Fatalf("failed to get job history: %v", err)
	}
	if len(hist) != 1 || !hist[0].Success {
		t.Fatalf("expected 1 successful history entry, got %+v", hist)
	}
}

func TestScheduledJob_DurableFields(t *testing.T) {
	repo, _ := setupTestSchedulerRepo(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Second)
	job := &ScheduledJob{
		ChatID:          12345,
		PeerType:        "user",
		AccessHash:      9999,
		ActionType:      "command",
		Payload:         ".ping",
		IntervalSeconds: 60,
		NextRunAt:       now,
		CreatedBy:       54321,
	}

	created, err := repo.CreateScheduledJob(ctx, job)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	if created.CreatedBy != 54321 {
		t.Fatalf("expected CreatedBy 54321, got %d", created.CreatedBy)
	}

	fetched, err := repo.GetScheduledJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if fetched.CreatedBy != 54321 {
		t.Errorf("expected fetched CreatedBy 54321, got %d", fetched.CreatedBy)
	}

	list, err := repo.ListScheduledJobs(ctx, 12345)
	if err != nil {
		t.Fatalf("failed to list jobs: %v", err)
	}
	if len(list) != 1 || list[0].CreatedBy != 54321 {
		t.Errorf("expected list to have CreatedBy 54321: %+v", list)
	}

	due, err := repo.ListDueScheduledJobs(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("failed to list due jobs: %v", err)
	}
	if len(due) != 1 || due[0].CreatedBy != 54321 {
		t.Errorf("expected due to have CreatedBy 54321: %+v", due)
	}
}

func TestRecordJobFailure(t *testing.T) {
	repo, _ := setupTestSchedulerRepo(t)
	ctx := context.Background()

	job := &ScheduledJob{
		ChatID:     999,
		PeerType:   "chat",
		ActionType: "message",
		Payload:    "test failure",
		NextRunAt:  time.Now(),
		CreatedBy:  111,
	}
	created, err := repo.CreateScheduledJob(ctx, job)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	// Record first failure
	if err := repo.RecordJobFailure(ctx, created.ID, "connection timeout"); err != nil {
		t.Fatalf("failed to record failure: %v", err)
	}

	fetched, err := repo.GetScheduledJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if fetched.AttemptCount != 1 {
		t.Errorf("expected AttemptCount 1, got %d", fetched.AttemptCount)
	}
	if fetched.LastError != "connection timeout" {
		t.Errorf("expected LastError 'connection timeout', got %q", fetched.LastError)
	}

	// Record second failure
	if err := repo.RecordJobFailure(ctx, created.ID, "rpc error: FLOOD_WAIT_300"); err != nil {
		t.Fatalf("failed to record second failure: %v", err)
	}
	fetched2, _ := repo.GetScheduledJob(ctx, created.ID)
	if fetched2.AttemptCount != 2 {
		t.Errorf("expected AttemptCount 2, got %d", fetched2.AttemptCount)
	}
	if fetched2.LastError != "rpc error: FLOOD_WAIT_300" {
		t.Errorf("expected LastError 'rpc error: FLOOD_WAIT_300', got %q", fetched2.LastError)
	}

	// Record failure on non-existent job
	if err := repo.RecordJobFailure(ctx, 9999999, "should fail"); err == nil {
		t.Errorf("expected error on non-existent job failure record")
	}
}

func TestScheduledJob_ClaimLeaseAndStateTransitions(t *testing.T) {
	repo, _ := setupTestSchedulerRepo(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Second)

	// 1. Create a one-shot job and a recurring job
	jobOneShot := &ScheduledJob{
		ChatID:          111,
		PeerType:        "chat",
		ActionType:      "message",
		Payload:         "one shot message",
		IntervalSeconds: 0,
		NextRunAt:       now,
		CreatedBy:       12345,
	}
	jobRecurring := &ScheduledJob{
		ChatID:          222,
		PeerType:        "user",
		ActionType:      "command",
		Payload:         ".status",
		IntervalSeconds: 60,
		NextRunAt:       now,
		CreatedBy:       12345,
	}

	createdOneShot, err := repo.CreateScheduledJob(ctx, jobOneShot)
	if err != nil {
		t.Fatalf("failed to create one shot job: %v", err)
	}
	createdRecurring, err := repo.CreateScheduledJob(ctx, jobRecurring)
	if err != nil {
		t.Fatalf("failed to create recurring job: %v", err)
	}

	// 2. Claim due jobs with 90s lease
	claimed, err := repo.ClaimDueScheduledJobs(ctx, now, 10, 90*time.Second)
	if err != nil {
		t.Fatalf("failed to claim due jobs: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("expected 2 claimed jobs, got %d", len(claimed))
	}

	var oneShotClaimToken, recClaimToken string
	for _, j := range claimed {
		if j.Status != JobStatusRunning {
			t.Errorf("expected status 'running', got %q", j.Status)
		}
		if j.AttemptCount != 1 {
			t.Errorf("expected attempt_count 1, got %d", j.AttemptCount)
		}
		if j.ClaimToken == "" {
			t.Errorf("expected non-empty claim token on claimed job %d", j.ID)
		}
		if j.LeaseUntil == nil || !j.LeaseUntil.After(now) {
			t.Errorf("expected lease_until in future, got %v", j.LeaseUntil)
		}
		if j.ID == createdOneShot.ID {
			oneShotClaimToken = j.ClaimToken
		} else if j.ID == createdRecurring.ID {
			recClaimToken = j.ClaimToken
		}
	}

	// 3. Trying to claim again immediately should return 0 jobs (both currently leased)
	claimedAgain, err := repo.ClaimDueScheduledJobs(ctx, now.Add(5*time.Second), 10, 90*time.Second)
	if err != nil {
		t.Fatalf("failed second claim: %v", err)
	}
	if len(claimedAgain) != 0 {
		t.Fatalf("expected 0 jobs claimed while lease active, got %d", len(claimedAgain))
	}

	// 4. Test failure with retry: Fail one-shot job (attempt 1) with correct token
	err = repo.FailScheduledJob(ctx, createdOneShot.ID, oneShotClaimToken, "temporary rpc fail", 50, 10*time.Second, false, now)
	if err != nil {
		t.Fatalf("failed to fail job: %v", err)
	}
	fetchedOneShot, err := repo.GetScheduledJob(ctx, createdOneShot.ID)
	if err != nil {
		t.Fatalf("failed to fetch failed job: %v", err)
	}
	if fetchedOneShot.Status != JobStatusPending {
		t.Errorf("expected status 'pending' after transient failure, got %q", fetchedOneShot.Status)
	}
	if fetchedOneShot.LastError != "temporary rpc fail" {
		t.Errorf("expected last error 'temporary rpc fail', got %q", fetchedOneShot.LastError)
	}
	if fetchedOneShot.LeaseUntil != nil {
		t.Errorf("expected lease_until to be cleared after failure, got %v", fetchedOneShot.LeaseUntil)
	}
	if fetchedOneShot.ClaimToken != "" {
		t.Errorf("expected claim_token to be cleared after failure, got %q", fetchedOneShot.ClaimToken)
	}

	// 5. Simulate retry attempts up to max_attempts (3)
	// Second attempt: claim at now + 10s
	claim2, err := repo.ClaimDueScheduledJobs(ctx, now.Add(10*time.Second), 10, 90*time.Second)
	if err != nil || len(claim2) != 1 {
		t.Fatalf("expected 1 job claimed on attempt 2, got %d (err: %v)", len(claim2), err)
	}
	if claim2[0].AttemptCount != 2 {
		t.Errorf("expected attempt 2, got %d", claim2[0].AttemptCount)
	}
	_ = repo.FailScheduledJob(ctx, createdOneShot.ID, claim2[0].ClaimToken, "fail 2", 60, 20*time.Second, false, now.Add(10*time.Second))

	// Third attempt: claim at now + 30s
	claim3, err := repo.ClaimDueScheduledJobs(ctx, now.Add(30*time.Second), 10, 90*time.Second)
	if err != nil || len(claim3) != 1 {
		t.Fatalf("expected 1 job claimed on attempt 3, got %d (err: %v)", len(claim3), err)
	}
	if claim3[0].AttemptCount != 3 {
		t.Errorf("expected attempt 3, got %d", claim3[0].AttemptCount)
	}
	// Third failure reaches max_attempts (3) -> enters 'failed' state (dead letter)
	_ = repo.FailScheduledJob(ctx, createdOneShot.ID, claim3[0].ClaimToken, "fail 3 (fatal)", 70, 40*time.Second, false, now.Add(30*time.Second))

	// 6. Test completion of recurring job (within its active 90s lease):
	err = repo.CompleteScheduledJob(ctx, createdRecurring.ID, recClaimToken, 100, now)
	if err != nil {
		t.Fatalf("failed to complete recurring job: %v", err)
	}
	fetchedRec, err := repo.GetScheduledJob(ctx, createdRecurring.ID)
	if err != nil {
		t.Fatalf("failed to fetch completed recurring job: %v", err)
	}
	if fetchedRec.Status != JobStatusPending {
		t.Errorf("expected recurring job to return to 'pending', got %q", fetchedRec.Status)
	}
	if fetchedRec.AttemptCount != 0 {
		t.Errorf("expected attempt_count reset to 0, got %d", fetchedRec.AttemptCount)
	}
	expectedNext := now.Add(60 * time.Second)
	if !fetchedRec.NextRunAt.Equal(expectedNext) {
		t.Errorf("expected next run %v, got %v", expectedNext, fetchedRec.NextRunAt)
	}

	// Claiming at 2 hours later should NOT pick up the dead letter job
	claimedDead, _ := repo.ClaimDueScheduledJobs(ctx, now.Add(2*time.Hour), 10, 90*time.Second)
	for _, j := range claimedDead {
		if j.ID == createdOneShot.ID {
			t.Fatalf("dead letter job should not be claimed")
		}
	}

	// 7. Test completion of one-shot job:
	oneShot2, _ := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:     333,
		ActionType: "message",
		Payload:    "one shot 2",
		NextRunAt:  now,
	})
	claimOS2, err := repo.ClaimDueScheduledJobs(ctx, now, 10, 90*time.Second)
	if err != nil || len(claimOS2) == 0 {
		t.Fatalf("failed to claim oneShot2: %v", err)
	}
	err = repo.CompleteScheduledJob(ctx, oneShot2.ID, claimOS2[0].ClaimToken, 80, now)
	if err != nil {
		t.Fatalf("failed to complete one-shot job: %v", err)
	}
	deletedJob, err := repo.GetScheduledJob(ctx, oneShot2.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deletedJob != nil {
		t.Errorf("expected completed one-shot job to be deleted from database, got %+v", deletedJob)
	}
}

func TestScheduledJob_FencingTokenAndMisfirePolicy(t *testing.T) {
	repo, _ := setupTestSchedulerRepo(t)
	ctx := context.Background()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	// 1. Test Fencing Token Protection (stale worker rejected)
	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          999,
		PeerType:        "chat",
		ActionType:      "message",
		Payload:         "fencing test",
		IntervalSeconds: 0,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	// Worker A claims job
	claimA, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 30*time.Second)
	if err != nil || len(claimA) != 1 {
		t.Fatalf("worker A failed to claim: %v", err)
	}
	tokenA := claimA[0].ClaimToken

	// Simulate lease expiry: 31 seconds later, Worker B reclaims the job
	tLater := now.Add(31 * time.Second)
	claimB, err := repo.ClaimDueScheduledJobs(ctx, tLater, 1, 30*time.Second)
	if err != nil || len(claimB) != 1 {
		t.Fatalf("worker B failed to reclaim expired job: %v", err)
	}
	tokenB := claimB[0].ClaimToken

	if tokenA == tokenB {
		t.Fatalf("tokens must be distinct between claims")
	}

	// Stale Worker A attempts to CompleteScheduledJob with tokenA -> MUST FAIL with ErrJobLeaseLost
	err = repo.CompleteScheduledJob(ctx, job.ID, tokenA, 50, tLater)
	if !errors.Is(err, ErrJobLeaseLost) {
		t.Errorf("expected ErrJobLeaseLost for stale Worker A Complete, got %v", err)
	}

	// Stale Worker A attempts to FailScheduledJob with tokenA -> MUST FAIL with ErrJobLeaseLost
	err = repo.FailScheduledJob(ctx, job.ID, tokenA, "error from stale worker", 50, 10*time.Second, false, tLater)
	if !errors.Is(err, ErrJobLeaseLost) {
		t.Errorf("expected ErrJobLeaseLost for stale Worker A Fail, got %v", err)
	}

	// Active Worker B completes job with tokenB -> MUST SUCCEED
	err = repo.CompleteScheduledJob(ctx, job.ID, tokenB, 50, tLater)
	if err != nil {
		t.Fatalf("active Worker B failed to complete job: %v", err)
	}

	// 2. Test Anchored Recurring Schedule and SkipMissed Policy
	anchorJob, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          888,
		PeerType:        "chat",
		ActionType:      "message",
		Payload:         "recurring anchor test",
		IntervalSeconds: 3600, // 1 hour
		NextRunAt:       now,  // 12:00
	})
	if err != nil {
		t.Fatalf("failed to create recurring job: %v", err)
	}

	// Normal run: runs at 12:00, finishes at 12:01
	claimNormal, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 90*time.Second)
	if err != nil || len(claimNormal) != 1 {
		t.Fatalf("failed to claim anchor job: %v", err)
	}
	finishNormal := now.Add(1 * time.Minute) // 12:01
	err = repo.CompleteScheduledJob(ctx, anchorJob.ID, claimNormal[0].ClaimToken, 60000, finishNormal)
	if err != nil {
		t.Fatalf("failed to complete anchor job: %v", err)
	}

	fetchedNormal, _ := repo.GetScheduledJob(ctx, anchorJob.ID)
	// Expected next run: strictly 13:00 (NOT 13:01!)
	expectedNextNormal := now.Add(1 * time.Hour)
	if !fetchedNormal.NextRunAt.Equal(expectedNextNormal) {
		t.Errorf("schedule drift detected! Expected %v, got %v", expectedNextNormal, fetchedNormal.NextRunAt)
	}

	// Simulated downtime: Bot went offline, resumes at 16:30
	downtimeNow := now.Add(4*time.Hour + 30*time.Minute) // 16:30
	claimCatchup, err := repo.ClaimDueScheduledJobs(ctx, downtimeNow, 1, 90*time.Second)
	if err != nil || len(claimCatchup) != 1 {
		t.Fatalf("failed to claim during catchup: %v", err)
	}
	finishCatchup := downtimeNow.Add(1 * time.Minute) // 16:31
	err = repo.CompleteScheduledJob(ctx, anchorJob.ID, claimCatchup[0].ClaimToken, 60000, finishCatchup)
	if err != nil {
		t.Fatalf("failed to complete catchup job: %v", err)
	}

	fetchedCatchup, _ := repo.GetScheduledJob(ctx, anchorJob.ID)
	// Anchored slot at 12:00 + N*1h that is > 16:31 is 17:00 (strictly on the hour!)
	expectedNextCatchup := now.Add(5 * time.Hour) // 17:00
	if !fetchedCatchup.NextRunAt.Equal(expectedNextCatchup) {
		t.Errorf("misfire catchup incorrect! Expected %v, got %v", expectedNextCatchup, fetchedCatchup.NextRunAt)
	}
}

func TestScheduledJob_AtomicStateHistoryAndPermanentError(t *testing.T) {
	repo, _ := setupTestSchedulerRepo(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	// 1. Recurring job completion writes history atomically
	recJob, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          111,
		PeerType:        "user",
		ActionType:      "message",
		Payload:         "atomic recurring test",
		IntervalSeconds: 60,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	claimed, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 30*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("failed to claim job: %v", err)
	}

	if err := repo.CompleteScheduledJob(ctx, recJob.ID, claimed[0].ClaimToken, 125, now); err != nil {
		t.Fatalf("failed to complete job: %v", err)
	}

	history, err := repo.GetJobHistory(ctx, recJob.ID, 10)
	if err != nil {
		t.Fatalf("failed to get job history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(history))
	}
	if !history[0].Success || history[0].DurationMs != 125 || history[0].ErrorMsg != "" {
		t.Errorf("unexpected history entry: %+v", history[0])
	}

	// 2. Permanent error bypasses retry and marks failed immediately
	permJob, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          222,
		PeerType:        "chat",
		ActionType:      "command",
		Payload:         ".help",
		IntervalSeconds: 0,
		NextRunAt:       now,
		MaxAttempts:     3,
	})
	if err != nil {
		t.Fatalf("failed to create permanent job: %v", err)
	}

	claimedPerm, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 30*time.Second)
	if err != nil || len(claimedPerm) != 1 {
		t.Fatalf("failed to claim perm job: %v", err)
	}

	// Fail on attempt 1 with isPermanent = true
	err = repo.FailScheduledJob(ctx, permJob.ID, claimedPerm[0].ClaimToken, "CHAT_WRITE_FORBIDDEN", 45, 10*time.Second, true, now)
	if err != nil {
		t.Fatalf("failed to fail permanent job: %v", err)
	}

	fetchedPerm, err := repo.GetScheduledJob(ctx, permJob.ID)
	if err != nil {
		t.Fatalf("failed to get permanent job: %v", err)
	}
	if fetchedPerm.Status != JobStatusFailed {
		t.Errorf("expected job status 'failed' immediately on permanent error, got %q", fetchedPerm.Status)
	}

	historyPerm, err := repo.GetJobHistory(ctx, permJob.ID, 10)
	if err != nil {
		t.Fatalf("failed to get history: %v", err)
	}
	if len(historyPerm) != 1 {
		t.Fatalf("expected 1 history entry for perm fail, got %d", len(historyPerm))
	}
	if historyPerm[0].Success || historyPerm[0].ErrorMsg != "CHAT_WRITE_FORBIDDEN" || historyPerm[0].DurationMs != 45 {
		t.Errorf("unexpected perm history: %+v", historyPerm[0])
	}
}

func TestScheduledJob_LeaseRecoveryAudit(t *testing.T) {
	repo, _ := setupTestSchedulerRepo(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          333,
		PeerType:        "user",
		ActionType:      "message",
		Payload:         "lease recovery audit test",
		IntervalSeconds: 0,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	// Worker 1 claims job with 5-second lease
	claim1, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 5*time.Second)
	if err != nil || len(claim1) != 1 {
		t.Fatalf("worker 1 failed to claim: %v", err)
	}

	// Advance time past lease expiration (10 seconds later)
	tLater := now.Add(10 * time.Second)

	// Worker 2 claims due jobs -> should reclaim the expired job AND record an audit history entry
	claim2, err := repo.ClaimDueScheduledJobs(ctx, tLater, 1, 30*time.Second)
	if err != nil || len(claim2) != 1 {
		t.Fatalf("worker 2 failed to reclaim expired job: %v", err)
	}
	if claim2[0].ID != job.ID {
		t.Fatalf("expected reclaimed job ID %d, got %d", job.ID, claim2[0].ID)
	}
	if claim2[0].LastError != "previous execution lease expired" {
		t.Errorf("expected LastError to record lease expiration, got %q", claim2[0].LastError)
	}

	// Check history: should have audit record for the abandoned run
	hist, err := repo.GetJobHistory(ctx, job.ID, 10)
	if err != nil {
		t.Fatalf("failed to get job history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("expected 1 history record for abandoned lease, got %d", len(hist))
	}
	if hist[0].Success {
		t.Errorf("expected failure record for abandoned lease, got success=true")
	}
}

func TestScheduledJob_RenewJobLease(t *testing.T) {
	repo, _ := setupTestSchedulerRepo(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	job, err := repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:          555,
		PeerType:        "user",
		ActionType:      "message",
		Payload:         "lease renew test",
		IntervalSeconds: 0,
		NextRunAt:       now,
	})
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	claimed, err := repo.ClaimDueScheduledJobs(ctx, now, 1, 30*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("failed to claim job: %v", err)
	}
	token := claimed[0].ClaimToken

	// 1. Valid token renews lease
	err = repo.RenewJobLease(ctx, job.ID, token, 90*time.Second, now)
	if err != nil {
		t.Fatalf("failed to renew job lease: %v", err)
	}

	fetched, err := repo.GetScheduledJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	expectedLease := now.Add(90 * time.Second)
	if fetched.LeaseUntil == nil || !fetched.LeaseUntil.Equal(expectedLease) {
		t.Errorf("expected lease %v, got %v", expectedLease, fetched.LeaseUntil)
	}

	// 2. Invalid/stale token fails with ErrJobLeaseLost
	err = repo.RenewJobLease(ctx, job.ID, "stale-token", 90*time.Second, now)
	if !errors.Is(err, ErrJobLeaseLost) {
		t.Errorf("expected ErrJobLeaseLost for invalid token, got %v", err)
	}
}
