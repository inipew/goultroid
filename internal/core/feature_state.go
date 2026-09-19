package core

import "sync/atomic"

type chatFeatureState struct {
	loaded  bool
	active  map[int64]struct{}
	unknown map[int64]struct{}
}

// ChatFeatureSnapshot is a lock-free read snapshot used by message routing to
// decide whether chat-scoped feature work can be skipped safely.
//
// Before the initial state is loaded, Interested fails open. Once loaded,
// inactive chats are skipped unless that individual chat has been marked
// unknown after a refresh failure.
type ChatFeatureSnapshot struct {
	state atomic.Pointer[chatFeatureState]
}

// ReplaceLoaded atomically replaces the full set of active chats after a
// successful persistent-state preload.
func (s *ChatFeatureSnapshot) ReplaceLoaded(chatIDs []int64) {
	active := make(map[int64]struct{}, len(chatIDs))
	for _, chatID := range chatIDs {
		if chatID != 0 {
			active[chatID] = struct{}{}
		}
	}
	s.state.Store(&chatFeatureState{loaded: true, active: active})
}

// Interested reports whether the feature may have state for chatID. Unknown
// global or per-chat state deliberately returns true (fail-open).
func (s *ChatFeatureSnapshot) Interested(chatID int64) bool {
	if chatID == 0 {
		return true
	}
	current := s.state.Load()
	if current == nil || !current.loaded {
		return true
	}
	if _, ok := current.unknown[chatID]; ok {
		return true
	}
	_, ok := current.active[chatID]
	return ok
}

// SetActive updates one chat after a successful mutation/readback. If the
// initial snapshot has not loaded yet, it stays globally unknown instead of
// manufacturing an incomplete known snapshot.
func (s *ChatFeatureSnapshot) SetActive(chatID int64, active bool) {
	if chatID == 0 {
		return
	}
	for {
		current := s.state.Load()
		if current == nil || !current.loaded {
			return
		}
		next := cloneChatFeatureState(current)
		if active {
			next.active[chatID] = struct{}{}
		} else {
			delete(next.active, chatID)
		}
		delete(next.unknown, chatID)
		if s.state.CompareAndSwap(current, next) {
			return
		}
	}
}

// MarkUnknown makes one chat fail-open without discarding the successfully
// loaded state of every other chat.
func (s *ChatFeatureSnapshot) MarkUnknown(chatID int64) {
	if chatID == 0 {
		return
	}
	for {
		current := s.state.Load()
		if current == nil || !current.loaded {
			return
		}
		next := cloneChatFeatureState(current)
		next.unknown[chatID] = struct{}{}
		if s.state.CompareAndSwap(current, next) {
			return
		}
	}
}

func cloneChatFeatureState(current *chatFeatureState) *chatFeatureState {
	next := &chatFeatureState{
		loaded:  current.loaded,
		active:  make(map[int64]struct{}, len(current.active)+1),
		unknown: make(map[int64]struct{}, len(current.unknown)+1),
	}
	for chatID := range current.active {
		next.active[chatID] = struct{}{}
	}
	for chatID := range current.unknown {
		next.unknown[chatID] = struct{}{}
	}
	return next
}
