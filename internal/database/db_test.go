package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
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

func TestDatabase_Concurrent(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cid := int64(1000 + idx)
			job := &ScheduledJob{
				ChatID:          cid,
				PeerType:        "chat",
				AccessHash:      0,
				ActionType:      "message",
				Payload:         "concurrent test",
				IntervalSeconds: 0,
				NextRunAt:       time.Now(),
			}
			_, _ = db.CreateScheduledJob(ctx, job)
			_, _ = db.ListScheduledJobs(ctx, cid)
		}(i)
	}
	wg.Wait()
}

func TestScheduledJobOperations(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	chatID := int64(998877)

	// 1. Initial list empty
	jobs, err := db.ListScheduledJobs(ctx, chatID)
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
	created1, err := db.CreateScheduledJob(ctx, job1)
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
	created2, err := db.CreateScheduledJob(ctx, job2)
	if err != nil {
		t.Fatalf("failed to create job2: %v", err)
	}

	// 4. Get job
	fetched, err := db.GetScheduledJob(ctx, created1.ID)
	if err != nil {
		t.Fatalf("failed to get job1: %v", err)
	}
	if fetched == nil || fetched.Payload != "Don't forget medicine!" || fetched.PeerType != "chat" {
		t.Fatalf("unexpected fetched job: %+v", fetched)
	}

	fetched2, err := db.GetScheduledJob(ctx, created2.ID)
	if err != nil || fetched2.AccessHash != 12345678 || fetched2.PeerType != "channel" {
		t.Fatalf("unexpected fetched job2: %+v", fetched2)
	}

	// 5. List jobs by chat
	all, err := db.ListScheduledJobs(ctx, chatID)
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
	due, err := db.ListDueScheduledJobs(ctx, now.Add(15*time.Minute))
	if err != nil {
		t.Fatalf("failed to list due jobs: %v", err)
	}
	if len(due) != 1 || due[0].ID != created1.ID {
		t.Fatalf("expected 1 due job (created1), got %d: %+v", len(due), due)
	}

	// 7. Update next run
	newNextRun := now.Add(2 * time.Hour)
	if err := db.UpdateScheduledJobNextRun(ctx, created2.ID, newNextRun); err != nil {
		t.Fatalf("failed to update next run: %v", err)
	}
	updated, _ := db.GetScheduledJob(ctx, created2.ID)
	if !updated.NextRunAt.Equal(newNextRun) {
		t.Errorf("expected NextRunAt %v, got %v", newNextRun, updated.NextRunAt)
	}

	// 8. Delete job
	if err := db.DeleteScheduledJob(ctx, created1.ID); err != nil {
		t.Fatalf("failed to delete job1: %v", err)
	}
	allAfter, _ := db.ListScheduledJobs(ctx, chatID)
	if len(allAfter) != 1 || allAfter[0].ID != created2.ID {
		t.Errorf("expected only created2 remaining, got %d", len(allAfter))
	}

	// 9. Delete non-existent job
	if err := db.DeleteScheduledJob(ctx, 999999); err == nil {
		t.Errorf("expected error deleting non-existent job")
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

func TestScheduledJob_DurableFields(t *testing.T) {
	db := setupTestDB(t)
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

	created, err := db.CreateScheduledJob(ctx, job)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	if created.CreatedBy != 54321 {
		t.Fatalf("expected CreatedBy 54321, got %d", created.CreatedBy)
	}

	fetched, err := db.GetScheduledJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if fetched.CreatedBy != 54321 {
		t.Errorf("expected fetched CreatedBy 54321, got %d", fetched.CreatedBy)
	}

	list, err := db.ListScheduledJobs(ctx, 12345)
	if err != nil {
		t.Fatalf("failed to list jobs: %v", err)
	}
	if len(list) != 1 || list[0].CreatedBy != 54321 {
		t.Errorf("expected list to have CreatedBy 54321: %+v", list)
	}

	due, err := db.ListDueScheduledJobs(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("failed to list due jobs: %v", err)
	}
	if len(due) != 1 || due[0].CreatedBy != 54321 {
		t.Errorf("expected due to have CreatedBy 54321: %+v", due)
	}
}

func TestRecordJobFailure(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	job := &ScheduledJob{
		ChatID:     999,
		PeerType:   "chat",
		ActionType: "message",
		Payload:    "test failure",
		NextRunAt:  time.Now(),
		CreatedBy:  111,
	}
	created, err := db.CreateScheduledJob(ctx, job)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	// Record first failure
	if err := db.RecordJobFailure(ctx, created.ID, "connection timeout"); err != nil {
		t.Fatalf("failed to record failure: %v", err)
	}

	fetched, err := db.GetScheduledJob(ctx, created.ID)
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
	if err := db.RecordJobFailure(ctx, created.ID, "rpc error: FLOOD_WAIT_300"); err != nil {
		t.Fatalf("failed to record second failure: %v", err)
	}
	fetched2, _ := db.GetScheduledJob(ctx, created.ID)
	if fetched2.AttemptCount != 2 {
		t.Errorf("expected AttemptCount 2, got %d", fetched2.AttemptCount)
	}
	if fetched2.LastError != "rpc error: FLOOD_WAIT_300" {
		t.Errorf("expected LastError 'rpc error: FLOOD_WAIT_300', got %q", fetched2.LastError)
	}

	// Record failure on non-existent job
	if err := db.RecordJobFailure(ctx, 9999999, "should fail"); err == nil {
		t.Errorf("expected error on non-existent job failure record")
	}
}

func TestScheduledJob_ClaimLeaseAndStateTransitions(t *testing.T) {
	db := setupTestDB(t)
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

	createdOneShot, err := db.CreateScheduledJob(ctx, jobOneShot)
	if err != nil {
		t.Fatalf("failed to create one shot job: %v", err)
	}
	createdRecurring, err := db.CreateScheduledJob(ctx, jobRecurring)
	if err != nil {
		t.Fatalf("failed to create recurring job: %v", err)
	}

	// 2. Claim due jobs with 90s lease
	claimed, err := db.ClaimDueScheduledJobs(ctx, now, 10, 90*time.Second)
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
	claimedAgain, err := db.ClaimDueScheduledJobs(ctx, now.Add(5*time.Second), 10, 90*time.Second)
	if err != nil {
		t.Fatalf("failed second claim: %v", err)
	}
	if len(claimedAgain) != 0 {
		t.Fatalf("expected 0 jobs claimed while lease active, got %d", len(claimedAgain))
	}

	// 4. Test failure with retry: Fail one-shot job (attempt 1) with correct token
	err = db.FailScheduledJob(ctx, createdOneShot.ID, oneShotClaimToken, "temporary rpc fail", 50, 10*time.Second, false, now)
	if err != nil {
		t.Fatalf("failed to fail job: %v", err)
	}
	fetchedOneShot, err := db.GetScheduledJob(ctx, createdOneShot.ID)
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
	claim2, err := db.ClaimDueScheduledJobs(ctx, now.Add(10*time.Second), 10, 90*time.Second)
	if err != nil || len(claim2) != 1 {
		t.Fatalf("expected 1 job claimed on attempt 2, got %d (err: %v)", len(claim2), err)
	}
	if claim2[0].AttemptCount != 2 {
		t.Errorf("expected attempt 2, got %d", claim2[0].AttemptCount)
	}
	_ = db.FailScheduledJob(ctx, createdOneShot.ID, claim2[0].ClaimToken, "fail 2", 60, 20*time.Second, false, now.Add(10*time.Second))

	// Third attempt: claim at now + 30s
	claim3, err := db.ClaimDueScheduledJobs(ctx, now.Add(30*time.Second), 10, 90*time.Second)
	if err != nil || len(claim3) != 1 {
		t.Fatalf("expected 1 job claimed on attempt 3, got %d (err: %v)", len(claim3), err)
	}
	if claim3[0].AttemptCount != 3 {
		t.Errorf("expected attempt 3, got %d", claim3[0].AttemptCount)
	}
	// Third failure reaches max_attempts (3) -> enters 'failed' state (dead letter)
	_ = db.FailScheduledJob(ctx, createdOneShot.ID, claim3[0].ClaimToken, "fail 3 (fatal)", 70, 40*time.Second, false, now.Add(30*time.Second))

	// 6. Test completion of recurring job (within its active 90s lease):
	err = db.CompleteScheduledJob(ctx, createdRecurring.ID, recClaimToken, 100, now)
	if err != nil {
		t.Fatalf("failed to complete recurring job: %v", err)
	}
	fetchedRec, err := db.GetScheduledJob(ctx, createdRecurring.ID)
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
	claimedDead, _ := db.ClaimDueScheduledJobs(ctx, now.Add(2*time.Hour), 10, 90*time.Second)
	for _, j := range claimedDead {
		if j.ID == createdOneShot.ID {
			t.Fatalf("dead letter job should not be claimed")
		}
	}

	// 7. Test completion of one-shot job:
	oneShot2, _ := db.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:     333,
		ActionType: "message",
		Payload:    "one shot 2",
		NextRunAt:  now,
	})
	claimOS2, err := db.ClaimDueScheduledJobs(ctx, now, 10, 90*time.Second)
	if err != nil || len(claimOS2) == 0 {
		t.Fatalf("failed to claim oneShot2: %v", err)
	}
	err = db.CompleteScheduledJob(ctx, oneShot2.ID, claimOS2[0].ClaimToken, 80, now)
	if err != nil {
		t.Fatalf("failed to complete one-shot job: %v", err)
	}
	deletedJob, err := db.GetScheduledJob(ctx, oneShot2.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deletedJob != nil {
		t.Errorf("expected completed one-shot job to be deleted from database, got %+v", deletedJob)
	}
}

func TestScheduledJob_FencingTokenAndMisfirePolicy(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	// 1. Test Fencing Token Protection (stale worker rejected)
	job, err := db.CreateScheduledJob(ctx, &ScheduledJob{
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
	claimA, err := db.ClaimDueScheduledJobs(ctx, now, 1, 30*time.Second)
	if err != nil || len(claimA) != 1 {
		t.Fatalf("worker A failed to claim: %v", err)
	}
	tokenA := claimA[0].ClaimToken

	// Simulate lease expiry: 31 seconds later, Worker B reclaims the job
	tLater := now.Add(31 * time.Second)
	claimB, err := db.ClaimDueScheduledJobs(ctx, tLater, 1, 30*time.Second)
	if err != nil || len(claimB) != 1 {
		t.Fatalf("worker B failed to reclaim expired job: %v", err)
	}
	tokenB := claimB[0].ClaimToken

	if tokenA == tokenB {
		t.Fatalf("tokens must be distinct between claims")
	}

	// Stale Worker A attempts to CompleteScheduledJob with tokenA -> MUST FAIL with ErrJobLeaseLost
	err = db.CompleteScheduledJob(ctx, job.ID, tokenA, 50, tLater)
	if !errors.Is(err, ErrJobLeaseLost) {
		t.Errorf("expected ErrJobLeaseLost for stale Worker A Complete, got %v", err)
	}

	// Stale Worker A attempts to FailScheduledJob with tokenA -> MUST FAIL with ErrJobLeaseLost
	err = db.FailScheduledJob(ctx, job.ID, tokenA, "error from stale worker", 50, 10*time.Second, false, tLater)
	if !errors.Is(err, ErrJobLeaseLost) {
		t.Errorf("expected ErrJobLeaseLost for stale Worker A Fail, got %v", err)
	}

	// Active Worker B completes job with tokenB -> MUST SUCCEED
	err = db.CompleteScheduledJob(ctx, job.ID, tokenB, 50, tLater)
	if err != nil {
		t.Fatalf("active Worker B failed to complete job: %v", err)
	}

	// 2. Test Anchored Recurring Schedule and SkipMissed Policy
	anchorJob, err := db.CreateScheduledJob(ctx, &ScheduledJob{
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
	claimNormal, err := db.ClaimDueScheduledJobs(ctx, now, 1, 90*time.Second)
	if err != nil || len(claimNormal) != 1 {
		t.Fatalf("failed to claim anchor job: %v", err)
	}
	finishNormal := now.Add(1 * time.Minute) // 12:01
	err = db.CompleteScheduledJob(ctx, anchorJob.ID, claimNormal[0].ClaimToken, 60000, finishNormal)
	if err != nil {
		t.Fatalf("failed to complete anchor job: %v", err)
	}

	fetchedNormal, _ := db.GetScheduledJob(ctx, anchorJob.ID)
	// Expected next run: strictly 13:00 (NOT 13:01!)
	expectedNextNormal := now.Add(1 * time.Hour)
	if !fetchedNormal.NextRunAt.Equal(expectedNextNormal) {
		t.Errorf("schedule drift detected! Expected %v, got %v", expectedNextNormal, fetchedNormal.NextRunAt)
	}

	// Simulated downtime: Bot went offline, resumes at 16:30
	downtimeNow := now.Add(4*time.Hour + 30*time.Minute) // 16:30
	claimCatchup, err := db.ClaimDueScheduledJobs(ctx, downtimeNow, 1, 90*time.Second)
	if err != nil || len(claimCatchup) != 1 {
		t.Fatalf("failed to claim during catchup: %v", err)
	}
	finishCatchup := downtimeNow.Add(1 * time.Minute) // 16:31
	err = db.CompleteScheduledJob(ctx, anchorJob.ID, claimCatchup[0].ClaimToken, 60000, finishCatchup)
	if err != nil {
		t.Fatalf("failed to complete catchup job: %v", err)
	}

	fetchedCatchup, _ := db.GetScheduledJob(ctx, anchorJob.ID)
	// Anchored slot at 12:00 + N*1h that is > 16:31 is 17:00 (strictly on the hour!)
	expectedNextCatchup := now.Add(5 * time.Hour) // 17:00
	if !fetchedCatchup.NextRunAt.Equal(expectedNextCatchup) {
		t.Errorf("misfire catchup incorrect! Expected %v, got %v", expectedNextCatchup, fetchedCatchup.NextRunAt)
	}
}

func TestScheduledJob_AtomicStateHistoryAndPermanentError(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	// 1. Recurring job completion writes history atomically
	recJob, err := db.CreateScheduledJob(ctx, &ScheduledJob{
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

	claimed, err := db.ClaimDueScheduledJobs(ctx, now, 1, 30*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("failed to claim job: %v", err)
	}

	if err := db.CompleteScheduledJob(ctx, recJob.ID, claimed[0].ClaimToken, 125, now); err != nil {
		t.Fatalf("failed to complete job: %v", err)
	}

	history, err := db.GetJobHistory(ctx, recJob.ID, 10)
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
	permJob, err := db.CreateScheduledJob(ctx, &ScheduledJob{
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

	claimedPerm, err := db.ClaimDueScheduledJobs(ctx, now, 1, 30*time.Second)
	if err != nil || len(claimedPerm) != 1 {
		t.Fatalf("failed to claim perm job: %v", err)
	}

	// Fail on attempt 1 with isPermanent = true
	err = db.FailScheduledJob(ctx, permJob.ID, claimedPerm[0].ClaimToken, "CHAT_WRITE_FORBIDDEN", 45, 10*time.Second, true, now)
	if err != nil {
		t.Fatalf("failed to fail permanent job: %v", err)
	}

	fetchedPerm, err := db.GetScheduledJob(ctx, permJob.ID)
	if err != nil {
		t.Fatalf("failed to get permanent job: %v", err)
	}
	if fetchedPerm.Status != JobStatusFailed {
		t.Errorf("expected job status 'failed' immediately on permanent error, got %q", fetchedPerm.Status)
	}

	historyPerm, err := db.GetJobHistory(ctx, permJob.ID, 10)
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
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	job, err := db.CreateScheduledJob(ctx, &ScheduledJob{
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
	claim1, err := db.ClaimDueScheduledJobs(ctx, now, 1, 5*time.Second)
	if err != nil || len(claim1) != 1 {
		t.Fatalf("worker 1 failed to claim: %v", err)
	}

	// Advance time past lease expiration (10 seconds later)
	tLater := now.Add(10 * time.Second)

	// Worker 2 claims due jobs -> should reclaim the expired job AND record an audit history entry
	claim2, err := db.ClaimDueScheduledJobs(ctx, tLater, 1, 30*time.Second)
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
	hist, err := db.GetJobHistory(ctx, job.ID, 10)
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
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	job, err := db.CreateScheduledJob(ctx, &ScheduledJob{
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

	claimed, err := db.ClaimDueScheduledJobs(ctx, now, 1, 30*time.Second)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("failed to claim job: %v", err)
	}
	token := claimed[0].ClaimToken

	// 1. Valid token renews lease
	err = db.RenewJobLease(ctx, job.ID, token, 90*time.Second, now)
	if err != nil {
		t.Fatalf("failed to renew job lease: %v", err)
	}

	fetched, err := db.GetScheduledJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	expectedLease := now.Add(90 * time.Second)
	if fetched.LeaseUntil == nil || !fetched.LeaseUntil.Equal(expectedLease) {
		t.Errorf("expected lease %v, got %v", expectedLease, fetched.LeaseUntil)
	}

	// 2. Invalid/stale token fails with ErrJobLeaseLost
	err = db.RenewJobLease(ctx, job.ID, "stale-token", 90*time.Second, now)
	if !errors.Is(err, ErrJobLeaseLost) {
		t.Errorf("expected ErrJobLeaseLost for invalid token, got %v", err)
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

func TestUserLogSettings(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	val, err := db.GetUserLogSetting(ctx, "log_chat_id")
	if err != nil {
		t.Fatalf("GetUserLogSetting failed: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty initial value, got %s", val)
	}

	if err := db.SetUserLogSetting(ctx, "log_chat_id", "-100123456789"); err != nil {
		t.Fatalf("SetUserLogSetting failed: %v", err)
	}

	val, err = db.GetUserLogSetting(ctx, "log_chat_id")
	if err != nil || val != "-100123456789" {
		t.Errorf("expected -100123456789, got %s (err=%v)", val, err)
	}
}

func TestAddonOperations(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Initial list empty
	addons, err := db.ListAddons(ctx)
	if err != nil {
		t.Fatalf("ListAddons failed: %v", err)
	}
	if len(addons) != 0 {
		t.Fatalf("expected 0 addons, got %d", len(addons))
	}

	// 2. Save addon
	now := time.Now().UTC()
	a1 := &AddonRecord{
		Name:         "weather-addon",
		Version:      "1.0.0",
		Description:  "Shows live weather data",
		Author:       "Alice",
		SourceURL:    "https://github.com/example/weather",
		Status:       "active",
		Capabilities: "telegram.send,media.download",
		MinVersion:   "0.5.0",
		InstalledAt:  now,
		UpdatedAt:    now,
	}
	if err := db.SaveAddon(ctx, a1); err != nil {
		t.Fatalf("SaveAddon failed: %v", err)
	}

	// 3. Get addon
	got, err := db.GetAddon(ctx, "weather-addon")
	if err != nil || got == nil {
		t.Fatalf("GetAddon failed: %v", err)
	}
	if got.Author != "Alice" || got.Status != "active" || got.Capabilities != "telegram.send,media.download" {
		t.Errorf("unexpected addon record: %+v", got)
	}

	// Case insensitive lookup
	got2, err := db.GetAddon(ctx, "WEATHER-ADDON")
	if err != nil || got2 == nil || got2.Name != "weather-addon" {
		t.Errorf("case-insensitive lookup failed: %+v (err=%v)", got2, err)
	}

	// 4. Update status
	if err := db.SetAddonStatus(ctx, "weather-addon", "disabled"); err != nil {
		t.Fatalf("SetAddonStatus failed: %v", err)
	}
	gotDisabled, _ := db.GetAddon(ctx, "weather-addon")
	if gotDisabled.Status != "disabled" {
		t.Errorf("expected status 'disabled', got %s", gotDisabled.Status)
	}

	// 5. Delete addon
	if err := db.DeleteAddon(ctx, "weather-addon"); err != nil {
		t.Fatalf("DeleteAddon failed: %v", err)
	}
	gotDeleted, _ := db.GetAddon(ctx, "weather-addon")
	if gotDeleted != nil {
		t.Errorf("expected nil after delete, got %+v", gotDeleted)
	}
}

func TestDBSettings(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Get non-existent setting
	item, err := db.GetSetting(ctx, "global", 0, "core", "prefix")
	if err != nil {
		t.Fatalf("expected nil error for missing setting, got: %v", err)
	}
	if item != nil {
		t.Fatalf("expected nil item, got %+v", item)
	}

	// 2. Set new setting
	now := time.Now().UTC()
	toSet := &SettingItem{
		ScopeType: "global",
		ScopeID:   0,
		Namespace: "core",
		Key:       "prefix",
		ValueType: "string",
		Value:     ".",
		UpdatedBy: 12345,
		UpdatedAt: now,
	}
	if err := db.SetSetting(ctx, toSet); err != nil {
		t.Fatalf("failed to set setting: %v", err)
	}

	// 3. Get setting
	item, err = db.GetSetting(ctx, "global", 0, "core", "prefix")
	if err != nil || item == nil {
		t.Fatalf("expected to find setting, err: %v, item: %+v", err, item)
	}
	if item.Value != "." || item.ValueType != "string" || item.UpdatedBy != 12345 {
		t.Errorf("unexpected setting content: %+v", item)
	}

	// 4. Update setting (generates second audit log)
	toSet.Value = "!"
	toSet.UpdatedBy = 67890
	if err := db.SetSetting(ctx, toSet); err != nil {
		t.Fatalf("failed to update setting: %v", err)
	}

	item, err = db.GetSetting(ctx, "global", 0, "core", "prefix")
	if err != nil || item == nil {
		t.Fatalf("failed to get updated setting: %v", err)
	}
	if item.Value != "!" || item.UpdatedBy != 67890 {
		t.Errorf("expected updated value '!', got %s", item.Value)
	}

	// 5. Add another setting in another namespace and chat scope
	chatItem := &SettingItem{
		ScopeType: "chat",
		ScopeID:   -100123456789,
		Namespace: "antispam",
		Key:       "enabled",
		ValueType: "bool",
		Value:     "true",
		UpdatedBy: 12345,
	}
	if err := db.SetSetting(ctx, chatItem); err != nil {
		t.Fatalf("failed to set chat setting: %v", err)
	}

	// 6. List settings
	listGlobal, err := db.ListSettings(ctx, "global", 0, "")
	if err != nil || len(listGlobal) != 1 {
		t.Fatalf("expected 1 global setting, got %d (err=%v)", len(listGlobal), err)
	}
	if listGlobal[0].Key != "prefix" {
		t.Errorf("expected prefix key, got %s", listGlobal[0].Key)
	}

	listChat, err := db.ListSettings(ctx, "chat", -100123456789, "antispam")
	if err != nil || len(listChat) != 1 {
		t.Fatalf("expected 1 chat setting, got %d (err=%v)", len(listChat), err)
	}
	if listChat[0].Key != "enabled" {
		t.Errorf("expected enabled key, got %s", listChat[0].Key)
	}

	// 7. Check audit history
	history, err := db.GetSettingHistory(ctx, "core", "prefix", 10)
	if err != nil {
		t.Fatalf("failed to get history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}
	// Most recent first: old_val=".", new_val="!"
	if history[0].OldVal != "." || history[0].NewVal != "!" || history[0].ChangedBy != 67890 {
		t.Errorf("unexpected latest history entry: %+v", history[0])
	}
	// Initial create: old_val="", new_val="."
	if history[1].OldVal != "" || history[1].NewVal != "." || history[1].ChangedBy != 12345 {
		t.Errorf("unexpected initial history entry: %+v", history[1])
	}

	// 8. Delete setting
	if err := db.DeleteSetting(ctx, "global", 0, "core", "prefix"); err != nil {
		t.Fatalf("failed to delete setting: %v", err)
	}
	deletedItem, err := db.GetSetting(ctx, "global", 0, "core", "prefix")
	if err != nil || deletedItem != nil {
		t.Fatalf("expected nil after delete, got item=%+v err=%v", deletedItem, err)
	}

	// History should now have 3 records (including delete)
	historyAfterDel, err := db.GetSettingHistory(ctx, "core", "prefix", 10)
	if err != nil {
		t.Fatalf("failed to get history after delete: %v", err)
	}
	if len(historyAfterDel) != 3 {
		t.Fatalf("expected 3 history records after delete, got %d", len(historyAfterDel))
	}
	if historyAfterDel[0].OldVal != "!" || historyAfterDel[0].NewVal != "" {
		t.Errorf("unexpected deletion history entry: %+v", historyAfterDel[0])
	}
}

func TestDB_SetSettingsBatch(t *testing.T) {
	db := setupTestDB(t)

	ctx := context.Background()

	batch := []*SettingItem{
		{
			ScopeType: "global",
			ScopeID:   0,
			Namespace: "system",
			Key:       "theme",
			ValueType: "string",
			Value:     "dark",
			UpdatedBy: 111,
		},
		{
			ScopeType: "global",
			ScopeID:   0,
			Namespace: "system",
			Key:       "notifications",
			ValueType: "bool",
			Value:     "true",
			UpdatedBy: 111,
		},
		{
			ScopeType: "chat",
			ScopeID:   -100999888,
			Namespace: "pmpermit",
			Key:       "warns",
			ValueType: "int",
			Value:     "5",
			UpdatedBy: 222,
		},
	}

	if err := db.SetSettingsBatch(ctx, batch); err != nil {
		t.Fatalf("failed to batch insert settings: %v", err)
	}

	// Verify all items were inserted
	item1, err := db.GetSetting(ctx, "global", 0, "system", "theme")
	if err != nil || item1 == nil || item1.Value != "dark" {
		t.Errorf("item1 mismatch: %+v, err: %v", item1, err)
	}

	item2, err := db.GetSetting(ctx, "global", 0, "system", "notifications")
	if err != nil || item2 == nil || item2.Value != "true" {
		t.Errorf("item2 mismatch: %+v, err: %v", item2, err)
	}

	item3, err := db.GetSetting(ctx, "chat", -100999888, "pmpermit", "warns")
	if err != nil || item3 == nil || item3.Value != "5" {
		t.Errorf("item3 mismatch: %+v, err: %v", item3, err)
	}

	// Empty batch should be no-op
	if err := db.SetSettingsBatch(ctx, nil); err != nil {
		t.Errorf("expected no error for empty batch, got: %v", err)
	}
}
