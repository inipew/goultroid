package voice

import (
	"context"
	"fmt"
	"sync"
)

// Backend abstracts the low-level voice call transport (WebRTC / tgcalls / RTP).
type Backend interface {
	// Join connects the client to the group voice call of the chat.
	Join(ctx context.Context, chatID int64) error
	// Leave disconnects the client from the voice call.
	Leave(ctx context.Context, chatID int64) error
	// Play streams the provided audio/video source to the voice call.
	Play(ctx context.Context, chatID int64, source Source) error
	// Pause pauses current audio streaming.
	Pause(ctx context.Context, chatID int64) error
	// Resume resumes paused streaming.
	Resume(ctx context.Context, chatID int64) error
	// Stop halts streaming.
	Stop(ctx context.Context, chatID int64) error
	// SetVolume sets playback volume (0-200).
	SetVolume(ctx context.Context, chatID int64, volume int) error
	// IsActive returns whether a voice call is active for this chat.
	IsActive(chatID int64) bool
}

// MockBackend provides an in-memory thread-safe voice call backend for tests and headless environments.
type MockBackend struct {
	mu           sync.RWMutex
	joinedChats  map[int64]bool
	activeSource map[int64]Source
	paused       map[int64]bool
	volumes      map[int64]int

	JoinErr  error
	PlayErr  error
	LeaveErr error
}

// NewMockBackend creates an initialized MockBackend.
func NewMockBackend() *MockBackend {
	return &MockBackend{
		joinedChats:  make(map[int64]bool),
		activeSource: make(map[int64]Source),
		paused:       make(map[int64]bool),
		volumes:      make(map[int64]int),
	}
}

// Join marks the chat as joined.
func (m *MockBackend) Join(ctx context.Context, chatID int64) error {
	if m.JoinErr != nil {
		return m.JoinErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.joinedChats[chatID] = true
	if _, ok := m.volumes[chatID]; !ok {
		m.volumes[chatID] = 100
	}
	return nil
}

// Leave marks the chat as left.
func (m *MockBackend) Leave(ctx context.Context, chatID int64) error {
	if m.LeaveErr != nil {
		return m.LeaveErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.joinedChats, chatID)
	delete(m.activeSource, chatID)
	delete(m.paused, chatID)
	return nil
}

// Play assigns the active source to the chat.
func (m *MockBackend) Play(ctx context.Context, chatID int64, source Source) error {
	if m.PlayErr != nil {
		return m.PlayErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.joinedChats[chatID] {
		m.joinedChats[chatID] = true
	}
	m.activeSource[chatID] = source
	m.paused[chatID] = false
	return nil
}

// Pause pauses streaming.
func (m *MockBackend) Pause(ctx context.Context, chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.joinedChats[chatID] {
		return fmt.Errorf("chat %d not joined", chatID)
	}
	m.paused[chatID] = true
	return nil
}

// Resume unpauses streaming.
func (m *MockBackend) Resume(ctx context.Context, chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.joinedChats[chatID] {
		return fmt.Errorf("chat %d not joined", chatID)
	}
	m.paused[chatID] = false
	return nil
}

// Stop removes the active source.
func (m *MockBackend) Stop(ctx context.Context, chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.activeSource, chatID)
	delete(m.paused, chatID)
	return nil
}

// SetVolume updates chat volume.
func (m *MockBackend) SetVolume(ctx context.Context, chatID int64, volume int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.volumes[chatID] = volume
	return nil
}

// IsActive returns whether the chat is connected.
func (m *MockBackend) IsActive(chatID int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.joinedChats[chatID]
}

// GetActiveSource inspects the currently playing source.
func (m *MockBackend) GetActiveSource(chatID int64) (Source, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.activeSource[chatID]
	return s, ok
}

// GetVolume gets the set volume.
func (m *MockBackend) GetVolume(chatID int64) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.volumes[chatID]; ok {
		return v
	}
	return 100
}

// IsPaused returns paused status.
func (m *MockBackend) IsPaused(chatID int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.paused[chatID]
}
