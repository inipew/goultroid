package core

import (
	"sync"
	"time"
)

// EventType identifies the kind of domain event.
type EventType string

const (
	// EventTypeMessageCreated is published when a new incoming message is received.
	EventTypeMessageCreated EventType = "message.created"
	// EventTypeMessageEdited is published when an existing message is edited.
	EventTypeMessageEdited EventType = "message.edited"
	// EventTypeMessagesDeleted is published when one or more messages are deleted.
	EventTypeMessagesDeleted EventType = "messages.deleted"
	// EventTypeCallbackQuery is published when an inline keyboard button callback query is received.
	EventTypeCallbackQuery EventType = "callback.query"
)

// Event is the base interface for all domain events.
type Event interface {
	// Type returns the event type discriminator.
	Type() EventType
	// Timestamp returns when the event was emitted.
	Timestamp() time.Time
}

// MessageCreatedEvent is emitted for every new incoming message dispatched by the bot.
type MessageCreatedEvent struct {
	At      time.Time
	Message *Message
	ChatID  int64
	PeerID  interface{} // tg.InputPeerClass — kept as interface{} to avoid circular tg import
}

func (e *MessageCreatedEvent) Type() EventType    { return EventTypeMessageCreated }
func (e *MessageCreatedEvent) Timestamp() time.Time { return e.At }

// MessageEditedEvent is emitted when a message in a chat the bot monitors is edited.
type MessageEditedEvent struct {
	At     time.Time
	MsgID  int
	ChatID int64
	Text   string
}

func (e *MessageEditedEvent) Type() EventType    { return EventTypeMessageEdited }
func (e *MessageEditedEvent) Timestamp() time.Time { return e.At }

// MessagesDeletedEvent is emitted when one or more messages are deleted.
type MessagesDeletedEvent struct {
	At     time.Time
	ChatID int64
	MsgIDs []int
}

func (e *MessagesDeletedEvent) Type() EventType    { return EventTypeMessagesDeleted }
func (e *MessagesDeletedEvent) Timestamp() time.Time { return e.At }

// CallbackQueryEvent is emitted when a user interacts with an inline keyboard button.
type CallbackQueryEvent struct {
	At      time.Time
	QueryID int64
	UserID  int64
	ChatID  int64
	MsgID   int
	Data    []byte
}

func (e *CallbackQueryEvent) Type() EventType     { return EventTypeCallbackQuery }
func (e *CallbackQueryEvent) Timestamp() time.Time { return e.At }

// EventHandler is a callback invoked when a subscribed event is published.
type EventHandler func(event Event)

// EventBus is a simple, thread-safe publish/subscribe event bus.
// Handlers are invoked asynchronously in separate goroutines so that a slow
// subscriber never blocks the publishing goroutine.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[EventType][]EventHandler
}

// NewEventBus creates and returns an empty EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[EventType][]EventHandler),
	}
}

// Subscribe registers handler to be called whenever an event of the given type is published.
// Multiple handlers for the same EventType are all called.
// Returns an unsubscribe function that removes this specific handler.
func (b *EventBus) Subscribe(t EventType, handler EventHandler) func() {
	if handler == nil {
		return func() {}
	}
	b.mu.Lock()
	b.subscribers[t] = append(b.subscribers[t], handler)
	// Capture position for removal
	idx := len(b.subscribers[t]) - 1
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		handlers := b.subscribers[t]
		if idx < len(handlers) {
			// Replace with nil to preserve indices of other handlers.
			handlers[idx] = nil
		}
	}
}

// Publish dispatches event to all registered handlers asynchronously.
// It is safe to call Publish from any goroutine.
func (b *EventBus) Publish(event Event) {
	if event == nil {
		return
	}
	b.mu.RLock()
	handlers := make([]EventHandler, len(b.subscribers[event.Type()]))
	copy(handlers, b.subscribers[event.Type()])
	b.mu.RUnlock()

	for _, h := range handlers {
		if h == nil {
			continue
		}
		go h(event)
	}
}
