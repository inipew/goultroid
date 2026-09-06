package core

import (
	"sync"
	"time"
)

type EventType string

const (
	EventTypeMessageCreated  EventType = "message.created"
	EventTypeMessageEdited   EventType = "message.edited"
	EventTypeMessagesDeleted EventType = "messages.deleted"
	EventTypeCallbackQuery   EventType = "callback.query"
	EventTypeReactionUpdated EventType = "reaction.updated"
)

type Event interface { Type() EventType; Timestamp() time.Time }

type MessageCreatedEvent struct { At time.Time; Message *Message; ChatID int64; PeerID interface{} }
func (e *MessageCreatedEvent) Type() EventType { return EventTypeMessageCreated }
func (e *MessageCreatedEvent) Timestamp() time.Time { return e.At }

type MessageEditedEvent struct { At time.Time; MsgID int; ChatID int64; Text string }
func (e *MessageEditedEvent) Type() EventType { return EventTypeMessageEdited }
func (e *MessageEditedEvent) Timestamp() time.Time { return e.At }

type MessagesDeletedEvent struct { At time.Time; ChatID int64; MsgIDs []int }
func (e *MessagesDeletedEvent) Type() EventType { return EventTypeMessagesDeleted }
func (e *MessagesDeletedEvent) Timestamp() time.Time { return e.At }

type CallbackQueryEvent struct { At time.Time; QueryID int64; UserID int64; ChatID int64; MsgID int; Data []byte }
func (e *CallbackQueryEvent) Type() EventType { return EventTypeCallbackQuery }
func (e *CallbackQueryEvent) Timestamp() time.Time { return e.At }

type ReactionUpdatedEvent struct { At time.Time; MsgID int; ChatID int64; Reaction string }
func (e *ReactionUpdatedEvent) Type() EventType { return EventTypeReactionUpdated }
func (e *ReactionUpdatedEvent) Timestamp() time.Time { return e.At }

type EventHandler func(event Event)

type eventJob struct { handler EventHandler; event Event }

const (
	eventQueueSize = 1024
	eventWorkers   = 8
)

type EventBus struct {
	mu          sync.RWMutex
	subscribers map[EventType][]EventHandler
	queue       chan eventJob
	workers     sync.WaitGroup
	closed      bool
}

func NewEventBus() *EventBus {
	b := &EventBus{subscribers: make(map[EventType][]EventHandler), queue: make(chan eventJob, eventQueueSize)}
	b.workers.Add(eventWorkers)
	for i := 0; i < eventWorkers; i++ { go b.worker() }
	return b
}

func (b *EventBus) worker() {
	for job := range b.queue {
		func() {
			defer func() { _ = recover() }()
			job.handler(job.event)
		}()
	}
	b.workers.Done()
}

// Subscribe adds a handler. After Close, subscriptions are rejected with a
// no-op unsubscribe function so no goroutine can be attached to a dead bus.
func (b *EventBus) Subscribe(t EventType, handler EventHandler) func() {
	if handler == nil { return func() {} }
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed { return func() {} }
	b.subscribers[t] = append(b.subscribers[t], handler)
	idx := len(b.subscribers[t]) - 1
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		handlers := b.subscribers[t]
		if idx < len(handlers) { handlers[idx] = nil }
	}
}

// Publish is intentionally non-blocking. Each subscriber is independently
// queued; a full queue drops only that subscriber's event instead of starving
// later subscribers. The read lock is held through enqueue to make Close and
// Publish mutually exclusive around channel shutdown.
func (b *EventBus) Publish(event Event) {
	if event == nil { return }
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed { return }
	for _, h := range b.subscribers[event.Type()] {
		if h == nil { continue }
		select {
		case b.queue <- eventJob{handler: h, event: event}:
		default:
			// Observational events are best-effort by design.
		}
	}
}

// Close stops accepting new work, drains already queued events, and waits for
// every worker to exit. It is idempotent and safe to call more than once.
func (b *EventBus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.subscribers = make(map[EventType][]EventHandler)
	close(b.queue)
	b.mu.Unlock()
	b.workers.Wait()
	return nil
}
