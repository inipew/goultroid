package voice_test

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	voiceSvc "github.com/inipew/goultroid/internal/voice"
	voicePlugin "github.com/inipew/goultroid/plugins/voice"
)

func setupTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := database.RunFeatureMigrations(context.Background(), db, voicePlugin.Module); err != nil {
		t.Fatalf("failed to apply migrations: %v", err)
	}
	return db
}

func TestSQLiteRepository_SessionOperations(t *testing.T) {
	db := setupTestDB(t)
	repo := voicePlugin.NewSQLiteRepository(db)
	ctx := context.Background()
	chatID := int64(-100111)

	// 1. Session initially nil
	sess, err := repo.GetVoiceSession(ctx, chatID)
	if err != nil {
		t.Fatalf("GetVoiceSession failed: %v", err)
	}
	if sess != nil {
		t.Fatalf("expected nil session, got %+v", sess)
	}

	// 2. Upsert session
	now := time.Now().UTC()
	err = repo.UpsertVoiceSession(ctx, &voiceSvc.VoiceSessionRecord{
		ChatID:     chatID,
		State:      "PLAYING",
		Volume:     120,
		RepeatMode: "track",
		UpdatedAt:  now,
	})
	if err != nil {
		t.Fatalf("UpsertVoiceSession failed: %v", err)
	}

	// 3. Retrieve upserted session
	sess, err = repo.GetVoiceSession(ctx, chatID)
	if err != nil || sess == nil {
		t.Fatalf("failed to retrieve upserted session: %v", err)
	}
	if sess.State != "PLAYING" || sess.Volume != 120 || sess.RepeatMode != "track" {
		t.Errorf("unexpected session record: %+v", sess)
	}

	// 4. Update session
	err = repo.UpsertVoiceSession(ctx, &voiceSvc.VoiceSessionRecord{
		ChatID:     chatID,
		State:      "PAUSED",
		Volume:     80,
		RepeatMode: "queue",
		UpdatedAt:  now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("UpsertVoiceSession update failed: %v", err)
	}

	sess, err = repo.GetVoiceSession(ctx, chatID)
	if err != nil || sess == nil {
		t.Fatalf("failed to retrieve updated session: %v", err)
	}
	if sess.State != "PAUSED" || sess.Volume != 80 || sess.RepeatMode != "queue" {
		t.Errorf("unexpected updated session record: %+v", sess)
	}
}

func TestSQLiteRepository_QueueOperations(t *testing.T) {
	db := setupTestDB(t)
	repo := voicePlugin.NewSQLiteRepository(db)
	ctx := context.Background()
	chatID := int64(-100222)

	// 1. Add tracks
	t1 := &voiceSvc.VoiceQueueRecord{
		ChatID:          chatID,
		Title:           "Song 1",
		Artist:          "Artist A",
		SourceURL:       "https://example.com/1.mp3",
		DurationSeconds: 180,
		SourceType:      "audio",
		RequesterID:     12345,
	}
	if err := repo.AddVoiceQueueTrack(ctx, t1); err != nil {
		t.Fatalf("AddVoiceQueueTrack failed: %v", err)
	}
	if t1.ID == 0 || t1.Position != 1 {
		t.Errorf("expected track 1 ID > 0 and pos 1, got ID=%d pos=%d", t1.ID, t1.Position)
	}

	t2 := &voiceSvc.VoiceQueueRecord{
		ChatID:          chatID,
		Title:           "Song 2",
		Artist:          "Artist B",
		SourceURL:       "https://example.com/2.mp3",
		DurationSeconds: 240,
		SourceType:      "audio",
		RequesterID:     12345,
	}
	if err := repo.AddVoiceQueueTrack(ctx, t2); err != nil {
		t.Fatalf("AddVoiceQueueTrack 2 failed: %v", err)
	}
	if t2.Position != 2 {
		t.Errorf("expected track 2 pos 2, got %d", t2.Position)
	}

	// 2. Get queue
	queue, err := repo.GetVoiceQueue(ctx, chatID)
	if err != nil {
		t.Fatalf("GetVoiceQueue failed: %v", err)
	}
	if len(queue) != 2 {
		t.Fatalf("expected 2 items in queue, got %d", len(queue))
	}
	if queue[0].Title != "Song 1" || queue[1].Title != "Song 2" {
		t.Errorf("unexpected queue ordering: %+v", queue)
	}

	// 3. Pop track
	popped, err := repo.PopVoiceQueueTrack(ctx, chatID)
	if err != nil || popped == nil {
		t.Fatalf("PopVoiceQueueTrack failed: %v", err)
	}
	if popped.Title != "Song 1" {
		t.Errorf("expected popped title 'Song 1', got '%s'", popped.Title)
	}

	// 4. Remaining track
	queue, _ = repo.GetVoiceQueue(ctx, chatID)
	if len(queue) != 1 || queue[0].Title != "Song 2" {
		t.Errorf("expected 1 remaining track ('Song 2'), got %+v", queue)
	}

	// 5. Delete track by ID
	if err := repo.DeleteVoiceQueueTrack(ctx, queue[0].ID); err != nil {
		t.Fatalf("DeleteVoiceQueueTrack failed: %v", err)
	}
	queue, _ = repo.GetVoiceQueue(ctx, chatID)
	if len(queue) != 0 {
		t.Errorf("expected empty queue after delete, got %d", len(queue))
	}

	// 6. Pop on empty queue returns nil
	poppedEmpty, err := repo.PopVoiceQueueTrack(ctx, chatID)
	if err != nil {
		t.Fatalf("PopVoiceQueueTrack on empty failed: %v", err)
	}
	if poppedEmpty != nil {
		t.Errorf("expected nil from empty pop, got %+v", poppedEmpty)
	}

	// 7. Clear queue
	_ = repo.AddVoiceQueueTrack(ctx, t1)
	_ = repo.AddVoiceQueueTrack(ctx, t2)
	if err := repo.ClearVoiceQueue(ctx, chatID); err != nil {
		t.Fatalf("ClearVoiceQueue failed: %v", err)
	}
	queue, _ = repo.GetVoiceQueue(ctx, chatID)
	if len(queue) != 0 {
		t.Errorf("expected empty queue after clear, got %d", len(queue))
	}
}
