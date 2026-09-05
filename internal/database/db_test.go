package database

import (
	"context"
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


