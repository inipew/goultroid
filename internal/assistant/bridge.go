package assistant

import (
	"context"
	"sync"
	"time"
)

// EventType categorizes cross-client bridge events.
type EventType string

const (
	EventAlert        EventType = "alert"
	EventNotification EventType = "notification"
	EventLogForward   EventType = "log_forward"
)

// Event represents an event communicated between the userbot and the assistant bot.
type Event struct {
	Type      EventType `json:"type"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	ChatID    int64     `json:"chat_id,omitempty"`
	UserID    int64     `json:"user_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// EventHandler handles bridged events.
type EventHandler func(ctx context.Context, e Event) error

// Bridge mediates communication between the userbot client and the assistant bot client.
type Bridge struct {
	handlers map[int64]EventHandler
	nextID   int64
	mu       sync.RWMutex
}

// NewBridge creates a new userbot <-> assistant bridge.
func NewBridge() *Bridge {
	return &Bridge{
		handlers: make(map[int64]EventHandler),
	}
}

// Subscribe registers an event listener on the bridge and returns an unsubscribe function.
func (b *Bridge) Subscribe(h EventHandler) func() {
	if h == nil {
		return func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.handlers[id] = h
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		delete(b.handlers, id)
		b.mu.Unlock()
	}
}

// Dispatch broadcasts an event across all bridge listeners.
func (b *Bridge) Dispatch(ctx context.Context, e Event) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}

	b.mu.RLock()
	handlers := make([]EventHandler, 0, len(b.handlers))
	for _, h := range b.handlers {
		handlers = append(handlers, h)
	}
	b.mu.RUnlock()

	for _, h := range handlers {
		go func(handler EventHandler) {
			_ = handler(ctx, e)
		}(h)
	}
}
