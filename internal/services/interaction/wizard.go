package interaction

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// WizardState represents persisted wizard session state.
// Data is map[string]string for MVP; typed access is via constants and helpers below to avoid magic strings.
type WizardState struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Total     int               `json:"total"`
	Current   int               `json:"current"`
	OwnerID   int64             `json:"owner"`
	ChatID    int64             `json:"chat"`
	Data      map[string]string `json:"data,omitempty"`
	ExpiresAt time.Time         `json:"expires_at"`
	Canceled  bool              `json:"canceled"`
}

// WizardData keys — use constants instead of raw strings to avoid magic strings.
const (
	WizardKeyChatID   = "chat_id"
	WizardKeyInterval = "interval"
	WizardKeyAction   = "action"
	WizardKeyConfirm  = "confirm"
)

// Get returns a wizard data value by key.
func (s *WizardState) Get(key string) (string, bool) {
	if s.Data == nil {
		return "", false
	}
	v, ok := s.Data[key]
	return v, ok
}

// Set sets a wizard data value.
func (s *WizardState) Set(key, val string) {
	if s.Data == nil {
		s.Data = make(map[string]string)
	}
	s.Data[key] = val
}

// WizardEngine manages multi-step interactive wizard sessions with state, validation, and lifecycle.
type WizardEngine struct {
	mu       sync.RWMutex
	sessions map[string]*WizardState
	ttl      time.Duration
}

// NewWizardEngine creates a wizard engine with given TTL for sessions.
func NewWizardEngine(ttl time.Duration) *WizardEngine {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &WizardEngine{
		sessions: make(map[string]*WizardState),
		ttl:      ttl,
	}
}

// Start creates a new wizard session.
func (e *WizardEngine) Start(ctx context.Context, id, title string, totalSteps int, ownerID, chatID int64) (*WizardState, error) {
	if totalSteps <= 0 {
		totalSteps = 1
	}
	if id == "" {
		return nil, fmt.Errorf("wizard id cannot be empty")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.sessions[id]; exists {
		return nil, fmt.Errorf("wizard session %q already exists", id)
	}
	st := &WizardState{
		ID:        id,
		Title:     title,
		Total:     totalSteps,
		Current:   1,
		OwnerID:   ownerID,
		ChatID:    chatID,
		Data:      make(map[string]string),
		ExpiresAt: time.Now().Add(e.ttl),
	}
	e.sessions[id] = st
	return st, nil
}

// Get retrieves a wizard session if not expired/canceled.
func (e *WizardEngine) Get(id string) (*WizardState, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	st, ok := e.sessions[id]
	if !ok {
		return nil, false
	}
	if st.Canceled || time.Now().After(st.ExpiresAt) {
		return nil, false
	}
	cp := *st
	return &cp, true
}

// Next advances the wizard to the next step.
func (e *WizardEngine) Next(id string) (*WizardState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.sessions[id]
	if !ok {
		return nil, fmt.Errorf("wizard session %q not found", id)
	}
	if st.Canceled {
		return nil, fmt.Errorf("wizard session %q canceled", id)
	}
	if time.Now().After(st.ExpiresAt) {
		delete(e.sessions, id)
		return nil, fmt.Errorf("wizard session %q expired", id)
	}
	if st.Current < st.Total {
		st.Current++
		st.ExpiresAt = time.Now().Add(e.ttl)
	}
	cp := *st
	return &cp, nil
}

// Prev moves back one step.
func (e *WizardEngine) Prev(id string) (*WizardState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.sessions[id]
	if !ok {
		return nil, fmt.Errorf("wizard session %q not found", id)
	}
	if st.Current > 1 {
		st.Current--
	}
	cp := *st
	return &cp, nil
}

// Cancel marks a wizard as canceled.
func (e *WizardEngine) Cancel(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.sessions[id]
	if !ok {
		return fmt.Errorf("wizard session %q not found", id)
	}
	st.Canceled = true
	return nil
}

// SetData stores a key-value in wizard session data.
func (e *WizardEngine) SetData(id, key, val string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.sessions[id]
	if !ok {
		return fmt.Errorf("wizard session %q not found", id)
	}
	if st.Data == nil {
		st.Data = make(map[string]string)
	}
	st.Data[key] = val
	return nil
}

// Prune removes expired or canceled sessions.
func (e *WizardEngine) Prune() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	pruned := 0
	for id, st := range e.sessions {
		if st.Canceled || now.After(st.ExpiresAt) {
			delete(e.sessions, id)
			pruned++
		}
	}
	return pruned
}

// Validate checks whether a transition is allowed for the given user/chat.
func (e *WizardEngine) Validate(id string, userID, chatID int64) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	st, ok := e.sessions[id]
	if !ok {
		return fmt.Errorf("wizard session %q not found", id)
	}
	if st.OwnerID != 0 && st.OwnerID != userID {
		return fmt.Errorf("wizard owner mismatch")
	}
	if st.ChatID != 0 && st.ChatID != chatID {
		return fmt.Errorf("wizard chat mismatch")
	}
	if st.Canceled {
		return fmt.Errorf("wizard canceled")
	}
	if time.Now().After(st.ExpiresAt) {
		return fmt.Errorf("wizard expired")
	}
	return nil
}
