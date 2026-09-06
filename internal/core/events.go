package core

import (
	"sync"
	"time"
)

type EventType string
const (
	EventTypeMessageCreated EventType = "message.created"
	EventTypeMessageEdited EventType = "message.edited"
	EventTypeMessagesDeleted EventType = "messages.deleted"
	EventTypeCallbackQuery EventType = "callback.query"
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

// EventBus uses a bounded worker pool. A subscriber can no longer create an
// unbounded goroutine stream during a Telegram update burst.
type EventBus struct {
	mu sync.RWMutex
	subscribers map[EventType][]EventHandler
	queue chan eventJob
}

type eventJob struct { handler EventHandler; event Event }

const eventQueueSize = 1024
const eventWorkers = 8

func NewEventBus() *EventBus {
	b := &EventBus{subscribers: make(map[EventType][]EventHandler), queue: make(chan eventJob, eventQueueSize)}
	for i := 0; i < eventWorkers; i++ { go b.worker() }
	return b
}

func (b *EventBus) worker() {
	for job := range b.queue {
		func() { defer func() { _ = recover() }(); job.handler(job.event) }()
	}
}

func (b *EventBus) Subscribe(t EventType, handler EventHandler) func() {
	if handler == nil { return func() {} }
	b.mu.Lock(); b.subscribers[t] = append(b.subscribers[t], handler); idx := len(b.subscribers[t])-1; b.mu.Unlock()
	return func() { b.mu.Lock(); defer b.mu.Unlock(); handlers := b.subscribers[t]; if idx < len(handlers) { handlers[idx] = nil } }
}

// Publish is intentionally non-blocking. When the bounded queue is saturated,
// observational events are dropped rather than slowing the Telegram update path.
func (b *EventBus) Publish(event Event) {
	if event == nil { return }
	b.mu.RLock(); handlers := append([]EventHandler(nil), b.subscribers[event.Type()]...); b.mu.RUnlock()
	for _, h := range handlers {
		if h == nil { continue }
		select { case b.queue <- eventJob{handler: h, event: event}: default: return }
	}
}
