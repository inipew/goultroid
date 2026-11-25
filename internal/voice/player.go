package voice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

type sessionTeardownProgress struct {
	sess      *Session
	stopped   bool // backend.Stop succeeded
	left      bool // backend.Leave succeeded
	persisted bool // persistSession succeeded
}

// Service coordinates voice chat sessions, queue progression, and backend streaming.
type Service struct {
	backend     Backend
	repo        Repository
	resolver    *Resolver
	logger      *zap.Logger
	idleTimeout time.Duration

	shutdownMu      sync.Mutex
	mu              sync.RWMutex
	sessions        map[int64]*Session
	pendingTeardown map[int64]*sessionTeardownProgress
	stopped         bool
	drained         bool
	activeOps       int
	activeZero      chan struct{}

	stopCh chan struct{}
}

// NewService creates a new voice Service coordinator.
func NewService(backend Backend, repo Repository, resolver *Resolver, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Service{
		backend:         backend,
		repo:            repo,
		resolver:        resolver,
		logger:          logger.Named("voice"),
		idleTimeout:     5 * time.Minute,
		sessions:        make(map[int64]*Session),
		pendingTeardown: make(map[int64]*sessionTeardownProgress),
		stopCh:          make(chan struct{}),
	}
	return s
}

// Resolver returns the media query resolver.
func (s *Service) Resolver() *Resolver {
	return s.resolver
}

// IsStopped returns true if the service has been shut down.
func (s *Service) IsStopped() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stopped
}

// StopCh returns a receive-only channel that is closed when service shutdown begins.
func (s *Service) StopCh() <-chan struct{} {
	return s.stopCh
}

// beginOp coordinates operation admission under s.mu.
// If s.stopped is true, ErrServiceStopped is returned immediately.
// Otherwise, s.activeOps is incremented and a release function is returned.
func (s *Service) beginOp(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil, ErrServiceStopped
	}

	if s.activeOps == 0 {
		s.activeZero = make(chan struct{})
	}
	s.activeOps++

	var once sync.Once
	release := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.activeOps--
			if s.activeOps == 0 && s.activeZero != nil {
				close(s.activeZero)
			}
		})
	}
	return release, nil
}

// GetOrCreateSession retrieves or instantiates a Session for the specified chat under ctx.
func (s *Service) GetOrCreateSession(ctx context.Context, chatID int64) (*Session, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	return s.getOrCreateSession(ctx, chatID)
}

func (s *Service) getOrCreateSession(ctx context.Context, chatID int64) (*Session, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil, ErrServiceStopped
	}

	sess, ok := s.sessions[chatID]
	if !ok {
		q, err := NewQueue(ctx, chatID, s.repo)
		if err != nil {
			return nil, fmt.Errorf("init voice queue: %w", err)
		}
		sess = NewSession(chatID, q)

		// Check persisted session
		if s.repo != nil {
			opCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			r, err := s.repo.GetVoiceSession(opCtx, chatID)
			if err != nil {
				return nil, fmt.Errorf("load voice session: %w", err)
			}
			if r != nil {
				_ = sess.SetVolume(r.Volume)
				sess.SetRepeatMode(RepeatMode(r.RepeatMode))
			}
		}

		s.sessions[chatID] = sess
	}
	return sess, nil
}

// Play queues or immediately streams a track to the group voice call.
// Returns (session, enqueued, error). If enqueued is true, track was added to pending queue.
func (s *Service) Play(ctx context.Context, chatID int64, source Source) (*Session, bool, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()

	sess, err := s.getOrCreateSession(ctx, chatID)
	if err != nil {
		return nil, false, err
	}

	state := sess.GetState()
	if state == StatePlaying || state == StatePaused {
		// Session is currently playing a track; add to queue
		if err := sess.Queue().Enqueue(ctx, source); err != nil {
			return nil, false, err
		}
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

	if err := s.persistSession(ctx, sess); err != nil {
		return sess, false, fmt.Errorf("persist voice session: %w", err)
	}

	s.logger.Info("started voice playback",
		zap.Int64("chat_id", chatID),
		zap.String("title", source.Title),
	)

	return sess, false, nil
}

// Pause pauses streaming in the chat.
func (s *Service) Pause(ctx context.Context, chatID int64) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()

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

	if err := s.persistSession(ctx, sess); err != nil {
		return fmt.Errorf("persist voice session: %w", err)
	}
	return nil
}

// Resume unpauses streaming in the chat.
func (s *Service) Resume(ctx context.Context, chatID int64) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()

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

	if err := s.persistSession(ctx, sess); err != nil {
		return fmt.Errorf("persist voice session: %w", err)
	}
	return nil
}

// Skip advances to the next track in the queue or loops current track according to repeat mode.
func (s *Service) Skip(ctx context.Context, chatID int64) (*Source, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

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
			if err := sess.Queue().Enqueue(ctx, *cur); err != nil {
				return nil, err
			}
		}
	}

	// Dequeue next track
	next, ok, err := sess.Queue().Dequeue(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		// Queue empty -> Stop playback
		_ = s.backend.Stop(ctx, chatID)
		_ = sess.SetState(StateIdle)
		sess.SetCurrent(nil)
		if err := s.persistSession(ctx, sess); err != nil {
			return nil, fmt.Errorf("persist voice session: %w", err)
		}
		return nil, nil
	}

	// Play next track
	if err := s.backend.Play(ctx, chatID, next); err != nil {
		_ = sess.SetState(StateFailed)
		return nil, fmt.Errorf("%w: failed to play next track: %v", ErrBackendFailed, err)
	}

	sess.SetCurrent(&next)
	_ = sess.SetState(StatePlaying)
	if err := s.persistSession(ctx, sess); err != nil {
		return &next, fmt.Errorf("persist voice session: %w", err)
	}

	return &next, nil
}

// Stop halts playback and clears the queue.
func (s *Service) Stop(ctx context.Context, chatID int64) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()

	sess, err := s.GetSession(chatID)
	if err != nil {
		return err
	}

	if err := sess.Queue().Clear(ctx); err != nil {
		return err
	}
	_ = s.backend.Stop(ctx, chatID)
	_ = sess.SetState(StateIdle)
	sess.SetCurrent(nil)
	if err := s.persistSession(ctx, sess); err != nil {
		return fmt.Errorf("persist voice session: %w", err)
	}
	return nil
}

// Leave stops playback and disconnects from the group voice call.
func (s *Service) Leave(ctx context.Context, chatID int64) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()

	sess, err := s.GetSession(chatID)
	if err != nil {
		return err
	}

	_ = sess.Queue().Clear(ctx)
	_ = s.backend.Stop(ctx, chatID)
	if err := s.backend.Leave(ctx, chatID); err != nil {
		s.logger.Warn("backend leave reported error", zap.Error(err))
	}
	_ = sess.SetState(StateIdle)
	sess.SetCurrent(nil)
	sess.Cancel()

	s.mu.Lock()
	delete(s.sessions, chatID)
	if s.pendingTeardown != nil {
		delete(s.pendingTeardown, chatID)
	}
	s.mu.Unlock()

	if err := s.persistSession(ctx, sess); err != nil {
		return fmt.Errorf("persist voice session: %w", err)
	}
	return nil
}

// SetVolume adjusts playback volume.
func (s *Service) SetVolume(ctx context.Context, chatID int64, volume int) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()

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

	if err := s.persistSession(ctx, sess); err != nil {
		return fmt.Errorf("persist voice session: %w", err)
	}
	return nil
}

// SetRepeatMode sets the repeat mode for the chat.
func (s *Service) SetRepeatMode(ctx context.Context, chatID int64, mode RepeatMode) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()

	sess, err := s.GetSession(chatID)
	if err != nil {
		return err
	}

	sess.SetRepeatMode(mode)
	if err := s.persistSession(ctx, sess); err != nil {
		return fmt.Errorf("persist voice session: %w", err)
	}
	return nil
}

// GetSession returns the active session for a chat.
func (s *Service) GetSession(chatID int64) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.stopped {
		return nil, ErrServiceStopped
	}

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

func (s *Service) persistSession(ctx context.Context, sess *Session) error {
	if s.repo == nil || sess == nil {
		return nil
	}
	if ctx == nil {
		return ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.repo.UpsertVoiceSession(opCtx, &VoiceSessionRecord{
		ChatID:     sess.ChatID(),
		State:      string(sess.GetState()),
		Volume:     sess.Volume(),
		RepeatMode: string(sess.RepeatMode()),
		UpdatedAt:  time.Now().UTC(),
	})
}

// Shutdown disconnects all active voice chat sessions on app shutdown.
// Teardown is serialized by shutdownMu, bounded by ctx, and retryable on partial failures:
// uncompleted backend stop/leave and session persistence steps are preserved and retried
// upon subsequent Shutdown invocations. drained is marked true strictly when all sessions
// have completed teardown without errors.
func (s *Service) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}

	s.shutdownMu.Lock()
	defer s.shutdownMu.Unlock()

	s.mu.Lock()
	if s.drained {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}

	activeCount := s.activeOps
	activeZero := s.activeZero
	s.mu.Unlock()

	if activeCount > 0 && activeZero != nil {
		select {
		case <-activeZero:
		default:
			select {
			case <-activeZero:
			case <-ctx.Done():
				s.mu.RLock()
				remaining := s.activeOps
				s.mu.RUnlock()
				return fmt.Errorf("voice shutdown deadline exceeded waiting for %d active operation(s): %w", remaining, ctx.Err())
			}
		}
	}

	s.mu.Lock()
	if s.pendingTeardown == nil {
		s.pendingTeardown = make(map[int64]*sessionTeardownProgress)
	}
	for chatID, sess := range s.sessions {
		if _, exists := s.pendingTeardown[chatID]; !exists {
			s.pendingTeardown[chatID] = &sessionTeardownProgress{sess: sess}
		}
	}
	chatIDs := make([]int64, 0, len(s.pendingTeardown))
	for chatID := range s.pendingTeardown {
		chatIDs = append(chatIDs, chatID)
	}
	sort.Slice(chatIDs, func(i, j int) bool { return chatIDs[i] < chatIDs[j] })
	s.mu.Unlock()

	var errs []error
	for _, chatID := range chatIDs {
		s.mu.Lock()
		p, exists := s.pendingTeardown[chatID]
		s.mu.Unlock()
		if !exists || p == nil {
			continue
		}

		if !p.stopped {
			if err := s.backend.Stop(ctx, chatID); err != nil {
				errs = append(errs, fmt.Errorf("backend stop chat %d: %w", chatID, err))
				continue
			}
			p.stopped = true
		}

		if !p.left {
			if err := s.backend.Leave(ctx, chatID); err != nil {
				errs = append(errs, fmt.Errorf("backend leave chat %d: %w", chatID, err))
				continue
			}
			p.left = true
		}

		_ = p.sess.SetState(StateIdle)
		p.sess.SetCurrent(nil)

		if !p.persisted {
			if err := s.persistSession(ctx, p.sess); err != nil {
				errs = append(errs, fmt.Errorf("persist idle session chat %d: %w", chatID, err))
			} else {
				p.persisted = true
			}
		}

		if p.stopped && p.left && p.persisted {
			p.sess.Cancel()
			s.mu.Lock()
			delete(s.pendingTeardown, chatID)
			delete(s.sessions, chatID)
			s.mu.Unlock()
		}
	}

	s.mu.Lock()
	if len(s.pendingTeardown) == 0 {
		s.drained = true
		s.sessions = make(map[int64]*Session)
	}
	s.mu.Unlock()

	if len(errs) > 0 {
		return fmt.Errorf("voice shutdown encountered errors: %w", errors.Join(errs...))
	}
	return nil
}
