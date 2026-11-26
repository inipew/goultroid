package voice_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/voice"
	"go.uber.org/zap"
)

func TestSession_StateTransitions(t *testing.T) {
	q, err := voice.NewQueue(context.Background(), -1001, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	ctx := context.Background()
	q, err := voice.NewQueue(ctx, -1002, nil)
	if err != nil {
		t.Fatal(err)
	}

	if q.Len() != 0 {
		t.Fatalf("expected empty queue, got %d", q.Len())
	}

	track1 := voice.Source{Title: "Track 1", Duration: 3 * time.Minute, ChatID: -1002}
	track2 := voice.Source{Title: "Track 2", Duration: 4 * time.Minute, ChatID: -1002}
	track3 := voice.Source{Title: "Track 3", Duration: 5 * time.Minute, ChatID: -1002}

	if err := q.Enqueue(ctx, track1); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, track2); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, track3); err != nil {
		t.Fatal(err)
	}

	if q.Len() != 3 {
		t.Fatalf("expected len 3, got %d", q.Len())
	}

	peek, ok := q.Peek()
	if !ok || peek.Title != "Track 1" {
		t.Fatalf("peek failed: got %+v (ok=%v)", peek, ok)
	}

	// Dequeue
	d1, ok, err := q.Dequeue(ctx)
	if err != nil || !ok || d1.Title != "Track 1" {
		t.Fatalf("dequeue 1 failed: got %+v, err=%v", d1, err)
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
	if err := q.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 0 {
		t.Fatalf("expected len 0 after clear, got %d", q.Len())
	}
	_, ok, err = q.Dequeue(ctx)
	if err != nil || ok {
		t.Fatalf("expected dequeue on empty queue to return false, nil; got ok=%v, err=%v", ok, err)
	}
}

type mockFailingVoiceRepo struct {
	mu              sync.Mutex
	failAdd         bool
	failPop         bool
	failClear       bool
	failUpsert      bool
	failUpsertCount int
	upsertCalls     int
	failGetSess     bool
	failGetQ        bool
}

func (m *mockFailingVoiceRepo) GetVoiceSession(ctx context.Context, chatID int64) (*voice.VoiceSessionRecord, error) {
	if m.failGetSess {
		return nil, errors.New("db get session failed")
	}
	return nil, nil
}

func (m *mockFailingVoiceRepo) UpsertVoiceSession(ctx context.Context, rec *voice.VoiceSessionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsertCalls++
	if m.failUpsertCount > 0 {
		m.failUpsertCount--
		return errors.New("db upsert session failed")
	}
	if m.failUpsert {
		return errors.New("db upsert session failed")
	}
	return nil
}

func (m *mockFailingVoiceRepo) GetVoiceQueue(ctx context.Context, chatID int64) ([]*voice.VoiceQueueRecord, error) {
	if m.failGetQ {
		return nil, errors.New("db get queue failed")
	}
	return nil, nil
}

func (m *mockFailingVoiceRepo) AddVoiceQueueTrack(ctx context.Context, track *voice.VoiceQueueRecord) error {
	if m.failAdd {
		return errors.New("db write failed")
	}
	return nil
}

func (m *mockFailingVoiceRepo) PopVoiceQueueTrack(ctx context.Context, chatID int64) (*voice.VoiceQueueRecord, error) {
	if m.failPop {
		return nil, errors.New("db pop failed")
	}
	return &voice.VoiceQueueRecord{}, nil
}

func (m *mockFailingVoiceRepo) DeleteVoiceQueueTrack(ctx context.Context, id int64) error {
	return nil
}

func (m *mockFailingVoiceRepo) ClearVoiceQueue(ctx context.Context, chatID int64) error {
	if m.failClear {
		return errors.New("db clear failed")
	}
	return nil
}

func TestQueue_PersistenceErrorMaintainsInMemoryConsistency(t *testing.T) {
	ctx := context.Background()
	repo := &mockFailingVoiceRepo{failAdd: true}
	q, err := voice.NewQueue(ctx, -1003, repo)
	if err != nil {
		t.Fatal(err)
	}

	track := voice.Source{Title: "Test", ChatID: -1003}
	err = q.Enqueue(ctx, track)
	if err == nil {
		t.Fatal("expected error on failing repo, got nil")
	}
	// In-memory queue must NOT have been modified
	if q.Len() != 0 {
		t.Fatalf("expected queue len 0 after failed enqueue, got %d", q.Len())
	}

	// Now allow add but fail pop
	repo.failAdd = false
	repo.failPop = true
	if err := q.Enqueue(ctx, track); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 1 {
		t.Fatalf("expected queue len 1, got %d", q.Len())
	}

	_, _, err = q.Dequeue(ctx)
	if err == nil {
		t.Fatal("expected error on failed pop, got nil")
	}
	// In-memory queue must NOT have lost the item on DB failure
	if q.Len() != 1 {
		t.Fatalf("expected queue len 1 after failed dequeue, got %d", q.Len())
	}

	// Now fail clear
	repo.failClear = true
	if err := q.Clear(ctx); err == nil {
		t.Fatal("expected error on failed clear, got nil")
	}
	// In-memory queue must NOT have been cleared
	if q.Len() != 1 {
		t.Fatalf("expected queue len 1 after failed clear, got %d", q.Len())
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

func TestQueue_ContextIntegrity(t *testing.T) {
	// Nil context rejection
	if _, err := voice.NewQueue(nil, -1004, nil); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from NewQueue with nil ctx, got %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := voice.NewQueue(canceledCtx, -1004, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from NewQueue with canceled ctx, got %v", err)
	}

	q, err := voice.NewQueue(context.Background(), -1004, nil)
	if err != nil {
		t.Fatal(err)
	}
	track := voice.Source{Title: "T", ChatID: -1004}

	// Enqueue nil/canceled
	if err := q.Enqueue(nil, track); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Enqueue with nil ctx, got %v", err)
	}
	if err := q.Enqueue(canceledCtx, track); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from Enqueue with canceled ctx, got %v", err)
	}

	// Dequeue nil/canceled
	if _, _, err := q.Dequeue(nil); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Dequeue with nil ctx, got %v", err)
	}
	if _, _, err := q.Dequeue(canceledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from Dequeue with canceled ctx, got %v", err)
	}

	// Clear nil/canceled
	if err := q.Clear(nil); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Clear with nil ctx, got %v", err)
	}
	if err := q.Clear(canceledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from Clear with canceled ctx, got %v", err)
	}
}

func TestPlayerService_ContextAndShutdownIntegrity(t *testing.T) {
	mockBackend := voice.NewMockBackend()
	svc := voice.NewService(mockBackend, nil, nil, zap.NewNop())
	chatID := int64(-1005)
	track := voice.Source{Title: "Song", ChatID: chatID}

	// Nil context checks
	if _, _, err := svc.Play(nil, chatID, track); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Play, got %v", err)
	}
	if err := svc.Pause(nil, chatID); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Pause, got %v", err)
	}
	if err := svc.Resume(nil, chatID); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Resume, got %v", err)
	}
	if _, err := svc.Skip(nil, chatID); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Skip, got %v", err)
	}
	if err := svc.Stop(nil, chatID); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Stop, got %v", err)
	}
	if err := svc.Leave(nil, chatID); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Leave, got %v", err)
	}
	if err := svc.SetVolume(nil, chatID, 80); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from SetVolume, got %v", err)
	}
	if err := svc.SetRepeatMode(nil, chatID, voice.RepeatTrack); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from SetRepeatMode, got %v", err)
	}
	if _, err := svc.GetOrCreateSession(nil, chatID); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from GetOrCreateSession, got %v", err)
	}
	if err := svc.Shutdown(nil); !errors.Is(err, voice.ErrNilContext) {
		t.Fatalf("expected ErrNilContext from Shutdown, got %v", err)
	}

	// Canceled context checks
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := svc.Play(canceledCtx, chatID, track); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from Play, got %v", err)
	}

	// Start playback normally
	ctx := context.Background()
	_, _, err := svc.Play(ctx, chatID, track)
	if err != nil {
		t.Fatalf("Play failed: %v", err)
	}

	// Shutdown service
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	if !svc.IsStopped() {
		t.Fatal("expected svc.IsStopped() == true")
	}

	// All operations after shutdown must return ErrServiceStopped without touching backend or DB
	if _, _, err := svc.Play(ctx, chatID, track); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from Play after shutdown, got %v", err)
	}
	if err := svc.Pause(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from Pause after shutdown, got %v", err)
	}
	if err := svc.Resume(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from Resume after shutdown, got %v", err)
	}
	if _, err := svc.Skip(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from Skip after shutdown, got %v", err)
	}
	if err := svc.Stop(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from Stop after shutdown, got %v", err)
	}
	if err := svc.Leave(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from Leave after shutdown, got %v", err)
	}
	if err := svc.SetVolume(ctx, chatID, 80); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from SetVolume after shutdown, got %v", err)
	}
	if err := svc.SetRepeatMode(ctx, chatID, voice.RepeatTrack); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from SetRepeatMode after shutdown, got %v", err)
	}
	if _, err := svc.GetOrCreateSession(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from GetOrCreateSession after shutdown, got %v", err)
	}
	if _, err := svc.GetSession(chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped from GetSession after shutdown, got %v", err)
	}
}

func TestPlayerService_PersistenceErrorHonestyAndStateConsistency(t *testing.T) {
	mockBackend := voice.NewMockBackend()
	repo := &mockFailingVoiceRepo{failUpsert: true}
	svc := voice.NewService(mockBackend, repo, nil, zap.NewNop())
	ctx := context.Background()
	chatID := int64(-1006)
	track := voice.Source{Title: "Song 1", ChatID: chatID}

	// 1. Play with failing persistence: backend joins and plays, but persistence error is returned honestly
	sess, enqueued, err := svc.Play(ctx, chatID, track)
	if err == nil {
		t.Fatal("expected persistence error on Play, got nil")
	}
	if enqueued {
		t.Fatal("expected enqueued=false")
	}
	// Verify honest state: backend IS active, memory state IS Playing (not falsely claiming rolled back)
	if !mockBackend.IsActive(chatID) {
		t.Fatal("backend must remain active even when session persistence failed")
	}
	if sess.GetState() != voice.StatePlaying {
		t.Fatalf("expected memory state PLAYING, got %s", sess.GetState())
	}

	// 2. Pause with failing persistence: backend pauses, memory state Paused, error reported
	if err := svc.Pause(ctx, chatID); err == nil {
		t.Fatal("expected persistence error on Pause, got nil")
	}
	if !mockBackend.IsPaused(chatID) {
		t.Fatal("backend must be paused")
	}
	if sess.GetState() != voice.StatePaused {
		t.Fatalf("expected memory state PAUSED, got %s", sess.GetState())
	}

	// 3. Resume with failing persistence: backend resumes, memory state Playing, error reported
	if err := svc.Resume(ctx, chatID); err == nil {
		t.Fatal("expected persistence error on Resume, got nil")
	}
	if mockBackend.IsPaused(chatID) {
		t.Fatal("backend must be resumed")
	}
	if sess.GetState() != voice.StatePlaying {
		t.Fatalf("expected memory state PLAYING, got %s", sess.GetState())
	}

	// 4. SetVolume with failing persistence: backend volume 120, memory volume 120, error reported
	if err := svc.SetVolume(ctx, chatID, 120); err == nil {
		t.Fatal("expected persistence error on SetVolume, got nil")
	}
	if mockBackend.GetVolume(chatID) != 120 {
		t.Fatalf("expected backend volume 120, got %d", mockBackend.GetVolume(chatID))
	}
	if sess.Volume() != 120 {
		t.Fatalf("expected memory volume 120, got %d", sess.Volume())
	}

	// 5. Shutdown with failing persistence: collects persistence error into joined shutdown error
	if err := svc.Shutdown(ctx); err == nil {
		t.Fatal("expected joined persistence error on Shutdown, got nil")
	}
}

type blockingMockBackend struct {
	*voice.MockBackend
	enteredPlay chan struct{}
	barrierPlay chan struct{}
}

func (b *blockingMockBackend) Play(ctx context.Context, chatID int64, source voice.Source) error {
	if b.enteredPlay != nil {
		select {
		case b.enteredPlay <- struct{}{}:
		default:
		}
	}
	if b.barrierPlay != nil {
		select {
		case <-b.barrierPlay:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.MockBackend.Play(ctx, chatID, source)
}

func TestPlayerService_ShutdownWaitsForInFlightOperation(t *testing.T) {
	mockBackend := voice.NewMockBackend()
	entered := make(chan struct{})
	barrier := make(chan struct{})
	blockingBackend := &blockingMockBackend{
		MockBackend: mockBackend,
		enteredPlay: entered,
		barrierPlay: barrier,
	}

	repo := &mockFailingVoiceRepo{}
	svc := voice.NewService(blockingBackend, repo, nil, zap.NewNop())
	ctx := context.Background()
	chatID := int64(-1007)
	track := voice.Source{Title: "In-flight Song", ChatID: chatID}

	// 1. Launch Play in background; it will block inside backend.Play at the barrier
	playErrCh := make(chan error, 1)
	go func() {
		_, _, err := svc.Play(ctx, chatID, track)
		playErrCh <- err
	}()

	// Wait until Play is actively executing inside the backend
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Play to enter backend barrier")
	}

	// 2. Concurrently invoke Shutdown in another goroutine
	shutdownErrCh := make(chan error, 1)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	go func() {
		shutdownErrCh <- svc.Shutdown(shutdownCtx)
	}()

	// 3. Synchronize explicitly that Shutdown has closed admission
	select {
	case <-svc.StopCh():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shutdown admission to close")
	}

	// Verify Shutdown does NOT return while the in-flight operation is held by the barrier
	select {
	case err := <-shutdownErrCh:
		t.Fatalf("Shutdown returned prematurely while operation was in-flight: %v", err)
	default:
		// Expected: Shutdown is waiting for the in-flight operation
	}

	// 4. While Shutdown is waiting, verify any new operation is immediately rejected with ErrServiceStopped
	if _, _, err := svc.Play(ctx, chatID, track); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on new Play while shutdown is in progress, got %v", err)
	}
	if err := svc.Pause(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on new Pause while shutdown is in progress, got %v", err)
	}
	if err := svc.Resume(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on new Resume while shutdown is in progress, got %v", err)
	}
	if _, err := svc.Skip(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on new Skip while shutdown is in progress, got %v", err)
	}
	if err := svc.Stop(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on new Stop while shutdown is in progress, got %v", err)
	}
	if err := svc.Leave(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on new Leave while shutdown is in progress, got %v", err)
	}
	if err := svc.SetVolume(ctx, chatID, 50); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on new SetVolume while shutdown is in progress, got %v", err)
	}
	if err := svc.SetRepeatMode(ctx, chatID, voice.RepeatTrack); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on new SetRepeatMode while shutdown is in progress, got %v", err)
	}
	if _, err := svc.GetOrCreateSession(ctx, chatID); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped on GetOrCreateSession while shutdown is in progress, got %v", err)
	}

	// 5. Release barrier so the in-flight Play can finish
	close(barrier)

	select {
	case err := <-playErrCh:
		if err != nil {
			t.Fatalf("Play failed after barrier release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-flight Play to finish")
	}

	// 6. Shutdown should now complete cleanly
	select {
	case err := <-shutdownErrCh:
		if err != nil {
			t.Fatalf("Shutdown failed after in-flight op finished: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Shutdown to complete")
	}

	// 7. Verify backend is no longer active
	if mockBackend.IsActive(chatID) {
		t.Fatal("expected backend to be torn down (inactive) after shutdown")
	}

	// 8. Verify post-shutdown fail-closed
	if _, _, err := svc.Play(ctx, chatID, track); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped after shutdown, got %v", err)
	}
}

func TestPlayerService_ShutdownDeadlineExceededOnStuckOperation(t *testing.T) {
	mockBackend := voice.NewMockBackend()
	entered := make(chan struct{})
	barrier := make(chan struct{})
	blockingBackend := &blockingMockBackend{
		MockBackend: mockBackend,
		enteredPlay: entered,
		barrierPlay: barrier,
	}

	repo := &mockFailingVoiceRepo{}
	svc := voice.NewService(blockingBackend, repo, nil, zap.NewNop())
	ctx := context.Background()
	chatID := int64(-1008)
	track := voice.Source{Title: "Stuck Song", ChatID: chatID}

	// 1. Launch Play in background; blocked at barrier
	playErrCh := make(chan error, 1)
	go func() {
		_, _, err := svc.Play(ctx, chatID, track)
		playErrCh <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Play to enter backend barrier")
	}

	// 2. Invoke Shutdown with a short deadline that expires before barrier release
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer shortCancel()

	err := svc.Shutdown(shortCtx)
	if err == nil {
		t.Fatal("expected shutdown deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded wrapped in shutdown error, got %v", err)
	}
	if !strings.Contains(err.Error(), "voice shutdown deadline exceeded waiting for 1 active operation(s)") {
		t.Fatalf("expected detailed active ops count in error message, got %v", err)
	}

	// 3. New operations must be rejected with ErrServiceStopped
	if _, _, err := svc.Play(ctx, chatID, track); !errors.Is(err, voice.ErrServiceStopped) {
		t.Fatalf("expected ErrServiceStopped after shutdown start, got %v", err)
	}

	// 4. Release barrier to clean up in-flight operation without leaking goroutines
	close(barrier)

	select {
	case err := <-playErrCh:
		if err != nil {
			t.Fatalf("Play failed after barrier release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-flight Play to finish")
	}

	// 5. Subsequent Shutdown with sufficient deadline should successfully complete draining
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer retryCancel()

	if err := svc.Shutdown(retryCtx); err != nil {
		t.Fatalf("retry Shutdown failed after operation finished: %v", err)
	}
	if mockBackend.IsActive(chatID) {
		t.Fatal("expected backend to be inactive after retry shutdown")
	}
}

type configurableMockBackend struct {
	*voice.MockBackend
	mu         sync.Mutex
	stopFails  map[int64]int
	leaveFails map[int64]int
	stopCalls  map[int64]int
	leaveCalls map[int64]int
}

func newConfigurableMockBackend() *configurableMockBackend {
	return &configurableMockBackend{
		MockBackend: voice.NewMockBackend(),
		stopFails:   make(map[int64]int),
		leaveFails:  make(map[int64]int),
		stopCalls:   make(map[int64]int),
		leaveCalls:  make(map[int64]int),
	}
}

func (c *configurableMockBackend) Stop(ctx context.Context, chatID int64) error {
	c.mu.Lock()
	c.stopCalls[chatID]++
	if c.stopFails[chatID] > 0 {
		c.stopFails[chatID]--
		c.mu.Unlock()
		return errors.New("backend stop simulated error")
	}
	c.mu.Unlock()
	return c.MockBackend.Stop(ctx, chatID)
}

func (c *configurableMockBackend) Leave(ctx context.Context, chatID int64) error {
	c.mu.Lock()
	c.leaveCalls[chatID]++
	if c.leaveFails[chatID] > 0 {
		c.leaveFails[chatID]--
		c.mu.Unlock()
		return errors.New("backend leave simulated error")
	}
	c.mu.Unlock()
	return c.MockBackend.Leave(ctx, chatID)
}

func (c *configurableMockBackend) getStopCalls(chatID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopCalls[chatID]
}

func (c *configurableMockBackend) getLeaveCalls(chatID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.leaveCalls[chatID]
}

func TestPlayerService_ShutdownTeardownRetryOnBackendStopFailure(t *testing.T) {
	backend := newConfigurableMockBackend()
	repo := &mockFailingVoiceRepo{}
	svc := voice.NewService(backend, repo, nil, zap.NewNop())
	ctx := context.Background()
	chatID := int64(-1010)
	track := voice.Source{Title: "Stop Test", ChatID: chatID}

	if _, _, err := svc.Play(ctx, chatID, track); err != nil {
		t.Fatalf("Play failed: %v", err)
	}

	// Configure Stop to fail once for chatID
	backend.mu.Lock()
	backend.stopFails[chatID] = 1
	backend.mu.Unlock()

	// 1. First shutdown: Stop fails -> Leave must NOT be called
	err := svc.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected error on first Shutdown when Stop fails, got nil")
	}
	if !strings.Contains(err.Error(), "backend stop chat -1010") {
		t.Fatalf("expected error mentioning backend stop failure, got: %v", err)
	}
	if backend.getStopCalls(chatID) != 1 {
		t.Fatalf("expected 1 stop call, got %d", backend.getStopCalls(chatID))
	}
	if backend.getLeaveCalls(chatID) != 0 {
		t.Fatalf("expected 0 leave calls while Stop() has not succeeded, got %d", backend.getLeaveCalls(chatID))
	}
	if !svc.IsStopped() {
		t.Fatal("expected svc.IsStopped() == true")
	}

	// 2. Retry shutdown: Stop retries and succeeds, then Leave is called
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("expected retry Shutdown to succeed, got %v", err)
	}
	if backend.getStopCalls(chatID) != 2 {
		t.Fatalf("expected 2 stop calls after retry, got %d", backend.getStopCalls(chatID))
	}
	if backend.getLeaveCalls(chatID) != 1 {
		t.Fatalf("expected 1 leave call after Stop() succeeded, got %d", backend.getLeaveCalls(chatID))
	}
	if backend.IsActive(chatID) {
		t.Fatal("expected backend to be inactive after successful retry shutdown")
	}

	// 3. Third shutdown (post-success): must be idempotent and return nil without repeating teardown
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("expected third Shutdown to return nil, got %v", err)
	}
	if backend.getStopCalls(chatID) != 2 {
		t.Fatalf("expected stop calls to remain 2, got %d", backend.getStopCalls(chatID))
	}
	if backend.getLeaveCalls(chatID) != 1 {
		t.Fatalf("expected leave calls to remain 1, got %d", backend.getLeaveCalls(chatID))
	}
}

func TestPlayerService_ShutdownTeardownRetryOnBackendLeaveFailure(t *testing.T) {
	backend := newConfigurableMockBackend()
	repo := &mockFailingVoiceRepo{}
	svc := voice.NewService(backend, repo, nil, zap.NewNop())
	ctx := context.Background()
	chatID := int64(-1011)
	track := voice.Source{Title: "Leave Test", ChatID: chatID}

	if _, _, err := svc.Play(ctx, chatID, track); err != nil {
		t.Fatalf("Play failed: %v", err)
	}

	// Configure Leave to fail once for chatID
	backend.mu.Lock()
	backend.leaveFails[chatID] = 1
	backend.mu.Unlock()

	// 1. First shutdown: Stop succeeds, Leave fails
	err := svc.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected error on first Shutdown when Leave fails, got nil")
	}
	if !strings.Contains(err.Error(), "backend leave chat -1011") {
		t.Fatalf("expected error mentioning backend leave failure, got: %v", err)
	}
	if backend.getStopCalls(chatID) != 1 {
		t.Fatalf("expected 1 stop call, got %d", backend.getStopCalls(chatID))
	}
	if backend.getLeaveCalls(chatID) != 1 {
		t.Fatalf("expected 1 leave call, got %d", backend.getLeaveCalls(chatID))
	}

	// 2. Retry shutdown: Stop was already completed so not called again; Leave is retried and succeeds
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("expected retry Shutdown to succeed, got %v", err)
	}
	if backend.getStopCalls(chatID) != 1 {
		t.Fatalf("expected stop calls to remain 1 (no duplicate stop), got %d", backend.getStopCalls(chatID))
	}
	if backend.getLeaveCalls(chatID) != 2 {
		t.Fatalf("expected 2 leave calls after retry, got %d", backend.getLeaveCalls(chatID))
	}

	// 3. Post-success idempotency: no side effects repeated
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("expected idempotent Shutdown to return nil, got %v", err)
	}
	if backend.getStopCalls(chatID) != 1 {
		t.Fatalf("expected stop calls to remain 1, got %d", backend.getStopCalls(chatID))
	}
	if backend.getLeaveCalls(chatID) != 2 {
		t.Fatalf("expected leave calls to remain 2, got %d", backend.getLeaveCalls(chatID))
	}
}

func TestPlayerService_ShutdownTeardownRetryOnPersistenceFailure(t *testing.T) {
	backend := newConfigurableMockBackend()
	repo := &mockFailingVoiceRepo{}
	svc := voice.NewService(backend, repo, nil, zap.NewNop())
	ctx := context.Background()
	chatID := int64(-1012)
	track := voice.Source{Title: "Persist Test", ChatID: chatID}

	if _, _, err := svc.Play(ctx, chatID, track); err != nil {
		t.Fatalf("Play failed: %v", err)
	}

	// Configure repo upsert to fail once during shutdown
	repo.mu.Lock()
	repo.failUpsertCount = 1
	repo.mu.Unlock()

	// 1. First shutdown: Stop and Leave succeed, idle session persistence fails
	err := svc.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected error on first Shutdown when persistSession fails, got nil")
	}
	if !strings.Contains(err.Error(), "persist idle session chat -1012") {
		t.Fatalf("expected error mentioning persist idle session failure, got: %v", err)
	}
	if backend.getStopCalls(chatID) != 1 {
		t.Fatalf("expected 1 stop call, got %d", backend.getStopCalls(chatID))
	}
	if backend.getLeaveCalls(chatID) != 1 {
		t.Fatalf("expected 1 leave call, got %d", backend.getLeaveCalls(chatID))
	}

	// 2. Retry shutdown: Stop and Leave were already completed so not called again; persistSession retries and succeeds
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("expected retry Shutdown to succeed, got %v", err)
	}
	if backend.getStopCalls(chatID) != 1 {
		t.Fatalf("expected stop calls to remain 1, got %d", backend.getStopCalls(chatID))
	}
	if backend.getLeaveCalls(chatID) != 1 {
		t.Fatalf("expected leave calls to remain 1, got %d", backend.getLeaveCalls(chatID))
	}

	// 3. Post-success idempotency: no side effects repeated
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("expected idempotent Shutdown to return nil, got %v", err)
	}
	if backend.getStopCalls(chatID) != 1 {
		t.Fatalf("expected stop calls to remain 1, got %d", backend.getStopCalls(chatID))
	}
	if backend.getLeaveCalls(chatID) != 1 {
		t.Fatalf("expected leave calls to remain 1, got %d", backend.getLeaveCalls(chatID))
	}
}

func TestPlayerService_ShutdownTeardownRetryMultipleSessionsPartialFailure(t *testing.T) {
	backend := newConfigurableMockBackend()
	repo := &mockFailingVoiceRepo{}
	svc := voice.NewService(backend, repo, nil, zap.NewNop())
	ctx := context.Background()
	chat1 := int64(-1013)
	chat2 := int64(-1014)

	if _, _, err := svc.Play(ctx, chat1, voice.Source{Title: "S1", ChatID: chat1}); err != nil {
		t.Fatalf("Play chat1 failed: %v", err)
	}
	if _, _, err := svc.Play(ctx, chat2, voice.Source{Title: "S2", ChatID: chat2}); err != nil {
		t.Fatalf("Play chat2 failed: %v", err)
	}

	// Configure chat2 Leave to fail once; chat1 succeeds completely
	backend.mu.Lock()
	backend.leaveFails[chat2] = 1
	backend.mu.Unlock()

	// 1. First shutdown: chat1 succeeds completely, chat2 fails on Leave
	err := svc.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected error on first Shutdown, got nil")
	}
	if !strings.Contains(err.Error(), "backend leave chat -1014") {
		t.Fatalf("expected error mentioning chat2 leave failure, got: %v", err)
	}
	// chat1 is fully completed
	if backend.getStopCalls(chat1) != 1 || backend.getLeaveCalls(chat1) != 1 {
		t.Fatalf("unexpected chat1 calls: stop=%d, leave=%d", backend.getStopCalls(chat1), backend.getLeaveCalls(chat1))
	}
	// chat2 had stop succeed, leave fail
	if backend.getStopCalls(chat2) != 1 || backend.getLeaveCalls(chat2) != 1 {
		t.Fatalf("unexpected chat2 calls: stop=%d, leave=%d", backend.getStopCalls(chat2), backend.getLeaveCalls(chat2))
	}

	// 2. Retry shutdown: only chat2 Leave is retried; chat1 is NOT called again!
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("expected retry Shutdown to succeed, got %v", err)
	}
	if backend.getStopCalls(chat1) != 1 || backend.getLeaveCalls(chat1) != 1 {
		t.Fatalf("chat1 was re-executed: stop=%d, leave=%d", backend.getStopCalls(chat1), backend.getLeaveCalls(chat1))
	}
	if backend.getStopCalls(chat2) != 1 {
		t.Fatalf("chat2 stop was unexpectedly re-executed: %d", backend.getStopCalls(chat2))
	}
	if backend.getLeaveCalls(chat2) != 2 {
		t.Fatalf("chat2 leave was not retried: %d", backend.getLeaveCalls(chat2))
	}

	// 3. Third shutdown: both chats already completed, returns nil immediately
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("expected third Shutdown to return nil, got %v", err)
	}
	if backend.getStopCalls(chat1) != 1 || backend.getLeaveCalls(chat1) != 1 {
		t.Fatalf("chat1 re-executed on third call")
	}
	if backend.getStopCalls(chat2) != 1 || backend.getLeaveCalls(chat2) != 2 {
		t.Fatalf("chat2 re-executed on third call")
	}
}
