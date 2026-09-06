package voice

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// SessionSnapshot contains read-only state information for UI presentation.
type SessionSnapshot struct {
	ChatID     int64         `json:"chat_id"`
	State      State         `json:"state"`
	Current    *Source       `json:"current"`
	QueueLen   int           `json:"queue_len"`
	Volume     int           `json:"volume"`
	RepeatMode RepeatMode    `json:"repeat_mode"`
	Elapsed    time.Duration `json:"elapsed"`
}

// Session tracks the active voice call state machine for a specific chat.
type Session struct {
	mu         sync.RWMutex
	chatID     int64
	state      State
	current    *Source
	queue      *Queue
	volume     int
	repeatMode RepeatMode
	startedAt  time.Time
	pausedAt   time.Time
	totalPause time.Duration

	ctx    context.Context
	cancel context.CancelFunc
}

// NewSession creates a new Session for a chat.
func NewSession(chatID int64, queue *Queue) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		chatID:     chatID,
		state:      StateIdle,
		queue:      queue,
		volume:     100,
		repeatMode: RepeatOff,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// ChatID returns the chat ID.
func (s *Session) ChatID() int64 {
	return s.chatID
}

// GetState returns the current state.
func (s *Session) GetState() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// SetState validates and applies a state transition.
func (s *Session) SetState(next State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == next {
		return nil
	}

	valid := false
	switch s.state {
	case StateIdle:
		valid = (next == StateJoining || next == StateFailed)
	case StateJoining:
		valid = (next == StatePlaying || next == StateFailed || next == StateIdle)
	case StatePlaying:
		valid = (next == StatePaused || next == StateStopping || next == StateFailed || next == StateIdle)
	case StatePaused:
		valid = (next == StatePlaying || next == StateStopping || next == StateFailed || next == StateIdle)
	case StateStopping:
		valid = (next == StateIdle || next == StateFailed)
	case StateFailed:
		valid = (next == StateIdle || next == StateJoining)
	default:
		valid = true
	}

	if !valid {
		return fmt.Errorf("%w: cannot transition from %s to %s", ErrInvalidStateTransition, s.state, next)
	}

	if next == StatePlaying && s.state == StatePaused {
		s.totalPause += time.Since(s.pausedAt)
	} else if next == StatePaused {
		s.pausedAt = time.Now()
	} else if next == StatePlaying && s.state != StatePaused {
		s.startedAt = time.Now()
		s.totalPause = 0
	} else if next == StateIdle {
		s.current = nil
		s.startedAt = time.Time{}
		s.totalPause = 0
	}

	s.state = next
	return nil
}

// Current returns the current playing track.
func (s *Session) Current() *Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// SetCurrent sets the currently playing track.
func (s *Session) SetCurrent(track *Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = track
	if track != nil {
		s.startedAt = time.Now()
		s.totalPause = 0
	}
}

// Volume returns the current playback volume.
func (s *Session) Volume() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.volume
}

// SetVolume sets playback volume (0-200).
func (s *Session) SetVolume(v int) error {
	if v < 0 || v > 200 {
		return ErrInvalidVolume
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volume = v
	return nil
}

// RepeatMode returns current repeat mode.
func (s *Session) RepeatMode() RepeatMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.repeatMode
}

// SetRepeatMode sets repeat mode.
func (s *Session) SetRepeatMode(mode RepeatMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repeatMode = mode
}

// Queue returns the session's track queue.
func (s *Session) Queue() *Queue {
	return s.queue
}

// Snapshot returns a point-in-time state representation.
func (s *Session) Snapshot() SessionSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var elapsed time.Duration
	if !s.startedAt.IsZero() {
		if s.state == StatePaused {
			elapsed = s.pausedAt.Sub(s.startedAt) - s.totalPause
		} else if s.state == StatePlaying {
			elapsed = time.Since(s.startedAt) - s.totalPause
		}
		if elapsed < 0 {
			elapsed = 0
		}
	}

	var curCopy *Source
	if s.current != nil {
		c := *s.current
		curCopy = &c
	}

	return SessionSnapshot{
		ChatID:     s.chatID,
		State:      s.state,
		Current:    curCopy,
		QueueLen:   s.queue.Len(),
		Volume:     s.volume,
		RepeatMode: s.repeatMode,
		Elapsed:    elapsed,
	}
}

// Cancel terminates session operations.
func (s *Session) Cancel() {
	s.cancel()
}
