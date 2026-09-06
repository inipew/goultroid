package voice

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

// Service coordinates voice chat sessions, queue progression, and backend streaming.
type Service struct {
	backend     Backend
	db          *database.DB
	resolver    *Resolver
	logger      *zap.Logger
	idleTimeout time.Duration

	mu       sync.RWMutex
	sessions map[int64]*Session

	stopCh chan struct{}
}

// NewService creates a new voice Service coordinator.
func NewService(backend Backend, db *database.DB, resolver *Resolver, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{
		backend:     backend,
		db:          db,
		resolver:    resolver,
		logger:      logger.Named("voice"),
		idleTimeout: 5 * time.Minute,
		sessions:    make(map[int64]*Session),
		stopCh:      make(chan struct{}),
	}
	return s
}

// Resolver returns the media query resolver.
func (s *Service) Resolver() *Resolver {
	return s.resolver
}

// GetOrCreateSession retrieves or instantiates a Session for the specified chat.
func (s *Service) GetOrCreateSession(chatID int64) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[chatID]
	if !ok {
		q := NewQueue(chatID, s.db)
		sess = NewSession(chatID, q)

		// Check persisted session
		if s.db != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if r, err := s.db.GetVoiceSession(ctx, chatID); err == nil && r != nil {
				_ = sess.SetVolume(r.Volume)
				sess.SetRepeatMode(RepeatMode(r.RepeatMode))
			}
		}

		s.sessions[chatID] = sess
	}
	return sess
}

// Play queues or immediately streams a track to the group voice call.
// Returns (session, enqueued, error). If enqueued is true, track was added to pending queue.
func (s *Service) Play(ctx context.Context, chatID int64, source Source) (*Session, bool, error) {
	sess := s.GetOrCreateSession(chatID)

	state := sess.GetState()
	if state == StatePlaying || state == StatePaused {
		// Session is currently playing a track; add to queue
		sess.Queue().Enqueue(source)
		s.logger.Info("enqueued track to voice queue",
			zap.Int64("chat_id", chatID),
			zap.String("title", source.Title),
			zap.Int("queue_len", sess.Queue().Len()),
		)
		return sess, true, nil
	}

	// Session is idle; transition to joining and stream track
	if err := sess.SetState(StateJoining); err != nil {
		return nil, false, err
	}

	// 1. Join chat VC if not active
	if !s.backend.IsActive(chatID) {
		if err := s.backend.Join(ctx, chatID); err != nil {
			_ = sess.SetState(StateFailed)
			return nil, false, fmt.Errorf("%w: failed to join voice chat: %v", ErrBackendFailed, err)
		}
	}

	// 2. Start streaming source
	if err := s.backend.Play(ctx, chatID, source); err != nil {
		_ = sess.SetState(StateFailed)
		return nil, false, fmt.Errorf("%w: failed to stream source: %v", ErrBackendFailed, err)
	}

	sess.SetCurrent(&source)
	if err := sess.SetState(StatePlaying); err != nil {
		return nil, false, err
	}

	s.persistSession(sess)
	s.logger.Info("started voice playback",
		zap.Int64("chat_id", chatID),
		zap.String("title", source.Title),
	)

	return sess, false, nil
}

// Pause pauses streaming in the chat.
func (s *Service) Pause(ctx context.Context, chatID int64) error {
	sess, err := s.GetSession(chatID)
	if err != nil {
		return err
	}

	if sess.GetState() != StatePlaying {
		return ErrNotPlaying
	}

	if err := s.backend.Pause(ctx, chatID); err != nil {
		return fmt.Errorf("%w: %v", ErrBackendFailed, err)
	}

	if err := sess.SetState(StatePaused); err != nil {
		return err
	}

	s.persistSession(sess)
	return nil
}

// Resume unpauses streaming in the chat.
func (s *Service) Resume(ctx context.Context, chatID int64) error {
	sess, err := s.GetSession(chatID)
	if err != nil {
		return err
	}

	if sess.GetState() != StatePaused {
		return fmt.Errorf("session is not paused")
	}

	if err := s.backend.Resume(ctx, chatID); err != nil {
		return fmt.Errorf("%w: %v", ErrBackendFailed, err)
	}

	if err := sess.SetState(StatePlaying); err != nil {
		return err
	}

	s.persistSession(sess)
	return nil
}

// Skip advances to the next track in the queue or loops current track according to repeat mode.
func (s *Service) Skip(ctx context.Context, chatID int64) (*Source, error) {
	sess, err := s.GetSession(chatID)
	if err != nil {
		return nil, err
	}

	cur := sess.Current()
	if cur == nil && sess.GetState() != StatePlaying {
		return nil, ErrNotPlaying
	}

	// Handle repeat mode
	switch sess.RepeatMode() {
	case RepeatTrack:
		if cur != nil {
			if err := s.backend.Play(ctx, chatID, *cur); err != nil {
				return nil, err
			}
			sess.SetCurrent(cur)
			return cur, nil
		}
	case RepeatQueue:
		if cur != nil {
			sess.Queue().Enqueue(*cur)
		}
	}

	// Dequeue next track
	next, ok := sess.Queue().Dequeue()
	if !ok {
		// Queue empty -> Stop playback
		_ = s.backend.Stop(ctx, chatID)
		_ = sess.SetState(StateIdle)
		sess.SetCurrent(nil)
		s.persistSession(sess)
		return nil, nil
	}

	// Play next track
	if err := s.backend.Play(ctx, chatID, next); err != nil {
		_ = sess.SetState(StateFailed)
		return nil, fmt.Errorf("%w: failed to play next track: %v", ErrBackendFailed, err)
	}

	sess.SetCurrent(&next)
	_ = sess.SetState(StatePlaying)
	s.persistSession(sess)

	return &next, nil
}

// Stop halts playback and clears the queue.
func (s *Service) Stop(ctx context.Context, chatID int64) error {
	sess, err := s.GetSession(chatID)
	if err != nil {
		return err
	}

	sess.Queue().Clear()
	_ = s.backend.Stop(ctx, chatID)
	_ = sess.SetState(StateIdle)
	sess.SetCurrent(nil)
	s.persistSession(sess)
	return nil
}

// Leave stops playback and disconnects from the group voice call.
func (s *Service) Leave(ctx context.Context, chatID int64) error {
	sess, err := s.GetSession(chatID)
	if err != nil {
		return err
	}

	sess.Queue().Clear()
	_ = s.backend.Stop(ctx, chatID)
	if err := s.backend.Leave(ctx, chatID); err != nil {
		s.logger.Warn("backend leave reported error", zap.Error(err))
	}
	_ = sess.SetState(StateIdle)
	sess.SetCurrent(nil)
	sess.Cancel()

	s.mu.Lock()
	delete(s.sessions, chatID)
	s.mu.Unlock()

	s.persistSession(sess)
	return nil
}

// SetVolume adjusts playback volume.
func (s *Service) SetVolume(ctx context.Context, chatID int64, volume int) error {
	sess, err := s.GetSession(chatID)
	if err != nil {
		return err
	}

	if err := sess.SetVolume(volume); err != nil {
		return err
	}

	if err := s.backend.SetVolume(ctx, chatID, volume); err != nil {
		return fmt.Errorf("%w: failed to set backend volume: %v", ErrBackendFailed, err)
	}

	s.persistSession(sess)
	return nil
}

// SetRepeatMode sets the repeat mode for the chat.
func (s *Service) SetRepeatMode(ctx context.Context, chatID int64, mode RepeatMode) error {
	sess, err := s.GetSession(chatID)
	if err != nil {
		return err
	}

	sess.SetRepeatMode(mode)
	s.persistSession(sess)
	return nil
}

// GetSession returns the active session for a chat.
func (s *Service) GetSession(chatID int64) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[chatID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return sess, nil
}

// GetQueue returns pending tracks for a chat.
func (s *Service) GetQueue(chatID int64) ([]Source, error) {
	sess, err := s.GetSession(chatID)
	if err != nil {
		return nil, err
	}
	return sess.Queue().List(), nil
}

func (s *Service) persistSession(sess *Session) {
	if s.db == nil || sess == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.db.UpsertVoiceSession(ctx, &database.VoiceSessionRecord{
			ChatID:     sess.ChatID(),
			State:      string(sess.GetState()),
			Volume:     sess.Volume(),
			RepeatMode: string(sess.RepeatMode()),
			UpdatedAt:  time.Now().UTC(),
		})
	}()
}

// Shutdown disconnects all active voice chat sessions on app shutdown.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for chatID, sess := range s.sessions {
		_ = s.backend.Stop(ctx, chatID)
		_ = s.backend.Leave(ctx, chatID)
		_ = sess.SetState(StateIdle)
		sess.Cancel()
	}
	s.sessions = make(map[int64]*Session)
	return nil
}
