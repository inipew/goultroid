package database

import (
	"context"
	"database/sql"
	"path/filepath"
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

func TestSudoOperations(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 1. Initial list should be empty
	users, err := db.GetSudoUsers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("expected 0 sudo users, got %d", len(users))
	}

	// 2. Add sudo user
	if err := db.AddSudoUser(ctx, 12345, 99999); err != nil {
		t.Fatalf("failed to add sudo user: %v", err)
	}
	if err := db.AddSudoUser(ctx, 67890, 99999); err != nil {
		t.Fatalf("failed to add second sudo user: %v", err)
	}

	// 3. Verify list
	users, err = db.GetSudoUsers(ctx)
	if err != nil {
		t.Fatalf("failed to get sudo users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 sudo users, got %d", len(users))
	}
	if users[0].UserID != 12345 || users[0].AddedBy != 99999 {
		t.Errorf("unexpected user 0: %+v", users[0])
	}

	// 4. Check existence
	isSudo, err := db.IsSudoUser(ctx, 12345)
	if err != nil || !isSudo {
		t.Errorf("expected 12345 to be sudo, got %v (err=%v)", isSudo, err)
	}
	isSudo, err = db.IsSudoUser(ctx, 11111)
	if err != nil || isSudo {
		t.Errorf("expected 11111 to NOT be sudo, got %v (err=%v)", isSudo, err)
	}

	// 5. Update existing sudo user
	if err := db.AddSudoUser(ctx, 12345, 88888); err != nil {
		t.Fatalf("failed to update sudo user: %v", err)
	}
	users, _ = db.GetSudoUsers(ctx)
	if len(users) != 2 {
		t.Fatalf("expected 2 sudo users, got %d", len(users))
	}
	found := false
	for _, u := range users {
		if u.UserID == 12345 {
			found = true
			if u.AddedBy != 88888 {
				t.Errorf("expected updated added_by 88888, got %d", u.AddedBy)
			}
		}
	}
	if !found {
		t.Errorf("expected user 12345 in sudo users list")
	}

	// 6. Remove sudo user
	if err := db.RemoveSudoUser(ctx, 12345); err != nil {
		t.Fatalf("failed to remove sudo user: %v", err)
	}
	isSudo, _ = db.IsSudoUser(ctx, 12345)
	if isSudo {
		t.Errorf("expected 12345 to be removed")
	}

	// 7. Remove non-existent returns error
	if err := db.RemoveSudoUser(ctx, 99999); err == nil {
		t.Errorf("expected error when removing non-existent user")
	}
}

func TestNotesOperations(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	chatID := int64(-100123456789)

	// 1. Initial list empty
	notes, err := db.ListNotes(ctx, chatID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("expected 0 notes, got %d", len(notes))
	}

	// 2. Save note
	if err := db.SaveNote(ctx, chatID, "welcome", "Hello and welcome!"); err != nil {
		t.Fatalf("failed to save note: %v", err)
	}
	if err := db.SaveNote(ctx, chatID, "rules", "No spamming."); err != nil {
		t.Fatalf("failed to save second note: %v", err)
	}

	// 3. Get note
	note, err := db.GetNote(ctx, chatID, "welcome")
	if err != nil {
		t.Fatalf("failed to get note: %v", err)
	}
	if note == nil || note.Content != "Hello and welcome!" {
		t.Fatalf("unexpected note content: %+v", note)
	}

	// 4. Get non-existent note
	missing, err := db.GetNote(ctx, chatID, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error getting missing note: %v", err)
	}
	if missing != nil {
		t.Errorf("expected nil note, got %+v", missing)
	}

	// 5. Update note
	if err := db.SaveNote(ctx, chatID, "welcome", "Updated welcome message!"); err != nil {
		t.Fatalf("failed to update note: %v", err)
	}
	note, _ = db.GetNote(ctx, chatID, "welcome")
	if note.Content != "Updated welcome message!" {
		t.Errorf("expected updated note, got %s", note.Content)
	}

	// 6. List notes
	notes, err = db.ListNotes(ctx, chatID)
	if err != nil {
		t.Fatalf("failed to list notes: %v", err)
	}
	if len(notes) != 2 || notes[0] != "rules" || notes[1] != "welcome" {
		t.Errorf("unexpected notes list: %v", notes)
	}

	// 7. Delete note
	if err := db.DeleteNote(ctx, chatID, "welcome"); err != nil {
		t.Fatalf("failed to delete note: %v", err)
	}
	notes, _ = db.ListNotes(ctx, chatID)
	if len(notes) != 1 || notes[0] != "rules" {
		t.Errorf("expected 1 note left, got %v", notes)
	}

	// 8. Delete non-existent note
	if err := db.DeleteNote(ctx, chatID, "welcome"); err == nil {
		t.Errorf("expected error deleting non-existent note")
	}
}

func TestAFKOperations(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	userID := int64(987654)

	// 1. Initial state not found
	status, err := db.GetAFK(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != nil {
		t.Fatalf("expected nil AFK status, got %+v", status)
	}

	// 2. Set AFK
	if err := db.SetAFK(ctx, userID, true, "Busy coding in Go"); err != nil {
		t.Fatalf("failed to set AFK: %v", err)
	}

	// 3. Get AFK
	status, err = db.GetAFK(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get AFK: %v", err)
	}
	if status == nil || !status.IsAFK || status.Reason != "Busy coding in Go" {
		t.Fatalf("unexpected AFK status: %+v", status)
	}
	if time.Since(status.Since) > 5*time.Second {
		t.Errorf("unexpected AFK since timestamp: %v", status.Since)
	}

	// 4. Deactivate AFK
	if err := db.SetAFK(ctx, userID, false, ""); err != nil {
		t.Fatalf("failed to turn off AFK: %v", err)
	}
	status, err = db.GetAFK(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get AFK after turn off: %v", err)
	}
	if status == nil || status.IsAFK {
		t.Errorf("expected AFK to be false, got %+v", status)
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
			uid := int64(1000 + idx)
			_ = db.AddSudoUser(ctx, uid, 1)
			_, _ = db.IsSudoUser(ctx, uid)
			_ = db.SaveNote(ctx, 123, "note", "content")
			_, _ = db.GetNote(ctx, 123, "note")
			_ = db.SetAFK(ctx, uid, true, "busy")
			_, _ = db.GetAFK(ctx, uid)
		}(i)
	}
	wg.Wait()
}

func TestFilterOperations(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	chatID := int64(-100123456789)

	// 1. Initial list empty
	filters, err := db.ListFilters(ctx, chatID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filters) != 0 {
		t.Fatalf("expected 0 filters, got %d", len(filters))
	}

	// 2. Save filter
	if err := db.SaveFilter(ctx, chatID, "hello", "Hello there!"); err != nil {
		t.Fatalf("failed to save filter: %v", err)
	}
	if err := db.SaveFilter(ctx, chatID, "rules", "Be kind."); err != nil {
		t.Fatalf("failed to save second filter: %v", err)
	}

	// 3. Get filter (case-insensitive)
	f, err := db.GetFilter(ctx, chatID, "HeLLo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f == nil || f.ReplyText != "Hello there!" {
		t.Errorf("unexpected filter result: %+v", f)
	}

	// 4. Overwrite filter
	if err := db.SaveFilter(ctx, chatID, "hello", "General Kenobi!"); err != nil {
		t.Fatalf("failed to overwrite filter: %v", err)
	}
	f, _ = db.GetFilter(ctx, chatID, "hello")
	if f == nil || f.ReplyText != "General Kenobi!" {
		t.Errorf("expected overwritten text 'General Kenobi!', got %+v", f)
	}

	// 5. List filters
	all, err := db.ListFilters(ctx, chatID)
	if err != nil || len(all) != 2 {
		t.Fatalf("expected 2 filters, got %d (err: %v)", len(all), err)
	}

	// 6. Delete filter
	if err := db.DeleteFilter(ctx, chatID, "rules"); err != nil {
		t.Fatalf("failed to delete filter: %v", err)
	}
	all, _ = db.ListFilters(ctx, chatID)
	if len(all) != 1 {
		t.Errorf("expected 1 filter after delete, got %d", len(all))
	}

	// 7. Delete non-existent
	if err := db.DeleteFilter(ctx, chatID, "rules"); err == nil {
		t.Errorf("expected error deleting non-existent filter")
	}
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

func TestBlacklistOperations(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	chatID := int64(112233)

	// 1. Initial list empty
	words, err := db.ListBlacklists(ctx, chatID)
	if err != nil {
		t.Fatalf("unexpected error listing blacklists: %v", err)
	}
	if len(words) != 0 {
		t.Fatalf("expected 0 words, got %d", len(words))
	}

	// 2. Add blacklist word
	if err := db.AddBlacklist(ctx, chatID, "spam"); err != nil {
		t.Fatalf("failed to add blacklist: %v", err)
	}
	if err := db.AddBlacklist(ctx, chatID, "scam"); err != nil {
		t.Fatalf("failed to add second blacklist: %v", err)
	}

	// 3. Add duplicate (upsert)
	if err := db.AddBlacklist(ctx, chatID, "SPAM"); err != nil {
		t.Fatalf("failed to upsert blacklist: %v", err)
	}

	// 4. List blacklists
	words, err = db.ListBlacklists(ctx, chatID)
	if err != nil {
		t.Fatalf("failed to list blacklists: %v", err)
	}
	if len(words) != 2 || words[0] != "scam" || words[1] != "spam" {
		t.Errorf("unexpected blacklist words: %v", words)
	}

	// 5. Remove blacklist
	if err := db.RemoveBlacklist(ctx, chatID, "spam"); err != nil {
		t.Fatalf("failed to remove blacklist: %v", err)
	}
	words, _ = db.ListBlacklists(ctx, chatID)
	if len(words) != 1 || words[0] != "scam" {
		t.Errorf("expected only 'scam' remaining, got %v", words)
	}

	// 6. Remove non-existent
	if err := db.RemoveBlacklist(ctx, chatID, "nonexistent"); err == nil {
		t.Errorf("expected error removing non-existent word")
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
	var migrations []mig
	for rows.Next() {
		var m mig
		if err := rows.Scan(&m.version, &m.desc); err != nil {
			t.Fatalf("failed to scan migration: %v", err)
		}
		migrations = append(migrations, m)
	}
	if len(migrations) != 3 {
		t.Fatalf("expected 3 applied migrations, got %d", len(migrations))
	}
	if migrations[0].version != 1 || migrations[1].version != 2 || migrations[2].version != 3 {
		t.Errorf("unexpected migration versions: %+v", migrations)
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

	// Verify v1, v2, and v3 are recorded
	var count int
	if err := rawDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("failed to count schema_migrations: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 migrations in legacy db after runMigrations, got %d", count)
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
		ChatID:          999,
		PeerType:        "chat",
		ActionType:      "message",
		Payload:         "test failure",
		NextRunAt:       time.Now(),
		CreatedBy:       111,
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

	// 2. Claim due jobs with 30s lease
	claimed, err := db.ClaimDueScheduledJobs(ctx, now, 10, 30*time.Second)
	if err != nil {
		t.Fatalf("failed to claim due jobs: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("expected 2 claimed jobs, got %d", len(claimed))
	}
	for _, j := range claimed {
		if j.Status != JobStatusRunning {
			t.Errorf("expected status 'running', got %q", j.Status)
		}
		if j.AttemptCount != 1 {
			t.Errorf("expected attempt_count 1, got %d", j.AttemptCount)
		}
		if j.LeaseUntil == nil || !j.LeaseUntil.After(now) {
			t.Errorf("expected lease_until in future, got %v", j.LeaseUntil)
		}
	}

	// 3. Trying to claim again immediately should return 0 jobs (both currently leased)
	claimedAgain, err := db.ClaimDueScheduledJobs(ctx, now.Add(5*time.Second), 10, 30*time.Second)
	if err != nil {
		t.Fatalf("failed second claim: %v", err)
	}
	if len(claimedAgain) != 0 {
		t.Fatalf("expected 0 jobs claimed while lease active, got %d", len(claimedAgain))
	}

	// 4. Test failure with retry: Fail one-shot job (attempt 1)
	err = db.FailScheduledJob(ctx, createdOneShot.ID, "temporary rpc fail", 10*time.Second, now)
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

	// 5. Simulate retry attempts up to max_attempts (3)
	// Second attempt: claim at now + 10s
	claim2, err := db.ClaimDueScheduledJobs(ctx, now.Add(10*time.Second), 10, 30*time.Second)
	if err != nil || len(claim2) != 1 {
		t.Fatalf("expected 1 job claimed on attempt 2, got %d (err: %v)", len(claim2), err)
	}
	if claim2[0].AttemptCount != 2 {
		t.Errorf("expected attempt 2, got %d", claim2[0].AttemptCount)
	}
	_ = db.FailScheduledJob(ctx, createdOneShot.ID, "fail 2", 20*time.Second, now.Add(10*time.Second))

	// Third attempt: claim at now + 30s
	claim3, err := db.ClaimDueScheduledJobs(ctx, now.Add(30*time.Second), 10, 30*time.Second)
	if err != nil || len(claim3) != 1 {
		t.Fatalf("expected 1 job claimed on attempt 3, got %d (err: %v)", len(claim3), err)
	}
	if claim3[0].AttemptCount != 3 {
		t.Errorf("expected attempt 3, got %d", claim3[0].AttemptCount)
	}
	// Third failure reaches max_attempts (3) -> enters 'failed' state (dead letter)
	_ = db.FailScheduledJob(ctx, createdOneShot.ID, "fail 3 (fatal)", 40*time.Second, now.Add(30*time.Second))

	fetchedDeadLetter, _ := db.GetScheduledJob(ctx, createdOneShot.ID)
	if fetchedDeadLetter.Status != JobStatusFailed {
		t.Errorf("expected dead-letter status 'failed', got %q", fetchedDeadLetter.Status)
	}

	// Claiming should NOT pick up the failed job anymore
	claimedDead, _ := db.ClaimDueScheduledJobs(ctx, now.Add(2*time.Hour), 10, 30*time.Second)
	for _, j := range claimedDead {
		if j.ID == createdOneShot.ID {
			t.Fatalf("dead letter job should not be claimed")
		}
	}

	// 6. Test completion of recurring job:
	err = db.CompleteScheduledJob(ctx, createdRecurring.ID, now)
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

	// 7. Test completion of one-shot job:
	// Create another one-shot job to complete
	oneShot2, _ := db.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID:     333,
		ActionType: "message",
		Payload:    "one shot 2",
		NextRunAt:  now,
	})
	err = db.CompleteScheduledJob(ctx, oneShot2.ID, now)
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




