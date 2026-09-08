package voice_test

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/voice"
	"go.uber.org/zap"
)

func TestSession_StateTransitions(t *testing.T) {
	q := voice.NewQueue(-1001, nil)
	s := voice.NewSession(-1001, q)

	if s.GetState() != voice.StateIdle {
		t.Fatalf("expected initial state IDLE, got %s", s.GetState())
	}

	// Valid transition: IDLE -> JOINING -> PLAYING
	if err := s.SetState(voice.StateJoining); err != nil {
		t.Fatalf("failed transition to JOINING: %v", err)
	}
	if err := s.SetState(voice.StatePlaying); err != nil {
		t.Fatalf("failed transition to PLAYING: %v", err)
	}

	// Invalid transition: PLAYING -> JOINING
	if err := s.SetState(voice.StateJoining); err == nil {
		t.Fatalf("expected error on PLAYING -> JOINING transition, got nil")
	}

	// Valid: PLAYING -> PAUSED -> PLAYING
	if err := s.SetState(voice.StatePaused); err != nil {
		t.Fatalf("failed transition to PAUSED: %v", err)
	}
	if err := s.SetState(voice.StatePlaying); err != nil {
		t.Fatalf("failed transition back to PLAYING: %v", err)
	}

	// Valid: PLAYING -> STOPPING -> IDLE
	if err := s.SetState(voice.StateStopping); err != nil {
		t.Fatalf("failed transition to STOPPING: %v", err)
	}
	if err := s.SetState(voice.StateIdle); err != nil {
		t.Fatalf("failed transition to IDLE: %v", err)
	}
}

func TestQueue_Operations(t *testing.T) {
	q := voice.NewQueue(-1002, nil)

	if q.Len() != 0 {
		t.Fatalf("expected empty queue, got %d", q.Len())
	}

	track1 := voice.Source{Title: "Track 1", Duration: 3 * time.Minute, ChatID: -1002}
	track2 := voice.Source{Title: "Track 2", Duration: 4 * time.Minute, ChatID: -1002}
	track3 := voice.Source{Title: "Track 3", Duration: 5 * time.Minute, ChatID: -1002}

	q.Enqueue(track1)
	q.Enqueue(track2)
	q.Enqueue(track3)

	if q.Len() != 3 {
		t.Fatalf("expected len 3, got %d", q.Len())
	}

	peek, ok := q.Peek()
	if !ok || peek.Title != "Track 1" {
		t.Fatalf("peek failed: got %+v (ok=%v)", peek, ok)
	}

	// Dequeue
	d1, ok := q.Dequeue()
	if !ok || d1.Title != "Track 1" {
		t.Fatalf("dequeue 1 failed: got %+v", d1)
	}
	if q.Len() != 2 {
		t.Fatalf("expected len 2 after dequeue, got %d", q.Len())
	}

	// List
	list := q.List()
	if len(list) != 2 || list[0].Title != "Track 2" {
		t.Fatalf("list unexpected: %+v", list)
	}

	// Clear
	q.Clear()
	if q.Len() != 0 {
		t.Fatalf("expected len 0 after clear, got %d", q.Len())
	}
	_, ok = q.Dequeue()
	if ok {
		t.Fatalf("expected dequeue on empty queue to return false")
	}
}

func TestPlayerService_PlayAndQueue(t *testing.T) {
	mockBackend := voice.NewMockBackend()
	resolver := voice.NewResolver(nil, nil)
	svc := voice.NewService(mockBackend, nil, resolver, zap.NewNop())
	ctx := context.Background()
	chatID := int64(-1003)

	t1 := voice.Source{Title: "Song A", Duration: 180 * time.Second, ChatID: chatID}
	t2 := voice.Source{Title: "Song B", Duration: 200 * time.Second, ChatID: chatID}

	// 1. Play first track -> immediately starts streaming
	sess, enqueued, err := svc.Play(ctx, chatID, t1)
	if err != nil {
		t.Fatalf("Play failed: %v", err)
	}
	if enqueued {
		t.Fatalf("expected first track to start immediately, not enqueued")
	}
	if sess.GetState() != voice.StatePlaying {
		t.Fatalf("expected state PLAYING, got %s", sess.GetState())
	}
	if !mockBackend.IsActive(chatID) {
		t.Fatalf("expected mock backend to have joined chat")
	}

	// 2. Play second track while first is playing -> enqueued
	sess, enqueued, err = svc.Play(ctx, chatID, t2)
	if err != nil {
		t.Fatalf("Play 2 failed: %v", err)
	}
	if !enqueued {
		t.Fatalf("expected second track to be enqueued")
	}
	if sess.Queue().Len() != 1 {
		t.Fatalf("expected queue length 1, got %d", sess.Queue().Len())
	}

	// 3. Pause & Resume
	if err := svc.Pause(ctx, chatID); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}
	if sess.GetState() != voice.StatePaused || !mockBackend.IsPaused(chatID) {
		t.Fatalf("expected state PAUSED and backend paused")
	}

	if err := svc.Resume(ctx, chatID); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if sess.GetState() != voice.StatePlaying || mockBackend.IsPaused(chatID) {
		t.Fatalf("expected state PLAYING and backend resumed")
	}

	// 4. Volume control
	if err := svc.SetVolume(ctx, chatID, 150); err != nil {
		t.Fatalf("SetVolume failed: %v", err)
	}
	if sess.Volume() != 150 || mockBackend.GetVolume(chatID) != 150 {
		t.Fatalf("volume not set properly: sess=%d, backend=%d", sess.Volume(), mockBackend.GetVolume(chatID))
	}
	if err := svc.SetVolume(ctx, chatID, 250); err == nil {
		t.Fatalf("expected error on volume > 200")
	}

	// 5. Skip to next track
	next, err := svc.Skip(ctx, chatID)
	if err != nil {
		t.Fatalf("Skip failed: %v", err)
	}
	if next == nil || next.Title != "Song B" {
		t.Fatalf("expected skipped track 'Song B', got %+v", next)
	}
	if sess.Queue().Len() != 0 {
		t.Fatalf("expected queue empty after skip, got %d", sess.Queue().Len())
	}

	// 6. Skip again on empty queue -> stops and goes to IDLE
	next, err = svc.Skip(ctx, chatID)
	if err != nil {
		t.Fatalf("second skip failed: %v", err)
	}
	if next != nil {
		t.Fatalf("expected nil from skipping with empty queue, got %+v", next)
	}
	if sess.GetState() != voice.StateIdle {
		t.Fatalf("expected state IDLE, got %s", sess.GetState())
	}

	// 7. Leave
	if err := svc.Leave(ctx, chatID); err != nil {
		t.Fatalf("Leave failed: %v", err)
	}
	if mockBackend.IsActive(chatID) {
		t.Fatalf("expected backend inactive after leave")
	}
}

func TestResolver_URLAndQuery(t *testing.T) {
	r := voice.NewResolver(nil, nil)
	ctx := context.Background()

	// 1. HTTP Audio URL
	s1, err := r.ResolveInput(ctx, nil, "https://example.com/audio/song.mp3")
	if err != nil {
		t.Fatalf("failed to resolve audio URL: %v", err)
	}
	if s1.Type != voice.SourceAudio || s1.Title != "song.mp3" {
		t.Errorf("unexpected resolved audio: %+v", s1)
	}

	// 2. HTTP Video URL
	s2, err := r.ResolveInput(ctx, nil, "https://example.com/video/movie.mp4")
	if err != nil {
		t.Fatalf("failed to resolve video URL: %v", err)
	}
	if s2.Type != voice.SourceVideo || s2.Title != "movie.mp4" {
		t.Errorf("unexpected resolved video: %+v", s2)
	}

	// 3. Search Query
	s3, err := r.ResolveInput(ctx, nil, "Never Gonna Give You Up")
	if err != nil {
		t.Fatalf("failed to resolve search query: %v", err)
	}
	if s3.Title != "Never Gonna Give You Up" {
		t.Errorf("unexpected search title: %s", s3.Title)
	}

	// 4. Empty query fails
	_, err = r.ResolveInput(ctx, nil, "   ")
	if err == nil {
		t.Fatalf("expected error on empty query, got nil")
	}
}

func TestPlayerService_Shutdown(t *testing.T) {
	mockBackend := voice.NewMockBackend()
	svc := voice.NewService(mockBackend, nil, nil, zap.NewNop())
	ctx := context.Background()

	_, _, err := svc.Play(ctx, -1001, voice.Source{Title: "Track 1", ChatID: -1001})
	if err != nil {
		t.Fatalf("Play failed: %v", err)
	}
	_, _, err = svc.Play(ctx, -1002, voice.Source{Title: "Track 2", ChatID: -1002})
	if err != nil {
		t.Fatalf("Play 2 failed: %v", err)
	}

	if !mockBackend.IsActive(-1001) || !mockBackend.IsActive(-1002) {
		t.Fatalf("expected both chats active")
	}

	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	if mockBackend.IsActive(-1001) || mockBackend.IsActive(-1002) {
		t.Fatalf("expected all chats inactive after shutdown")
	}
}
