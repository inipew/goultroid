package core

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
)

type EventType string

const (
	EventTypeMessageCreated  EventType = "message.created"
	EventTypeMessageEdited   EventType = "message.edited"
	EventTypeMessagesDeleted EventType = "messages.deleted"
	EventTypeCallbackQuery   EventType = "callback.query"
	EventTypeReactionUpdated EventType = "reaction.updated"
	EventTypeInlineChosen    EventType = "inline.chosen"
	EventTypeAdminAction     EventType = "admin.action"
	EventTypePMPermit        EventType = "pmpermit.action"
)

type Event interface{ Type() EventType; Timestamp() time.Time }

type MessageCreatedEvent struct{ At time.Time; Message *Message; ChatID int64; PeerID interface{} }
func (e *MessageCreatedEvent) Type() EventType { return EventTypeMessageCreated }
func (e *MessageCreatedEvent) Timestamp() time.Time { return e.At }

type MessageEditedEvent struct{ At time.Time; MsgID int; ChatID int64; Text string }
func (e *MessageEditedEvent) Type() EventType { return EventTypeMessageEdited }
func (e *MessageEditedEvent) Timestamp() time.Time { return e.At }

type MessagesDeletedEvent struct {
	At time.Time
	// ChatID is 0 when Telegram did not provide chat context (e.g. UpdateDeleteMessages).
	ChatID int64
	// PeerUnknown is true when ChatID is unknown / not provided by the update.
	PeerUnknown bool
	MsgIDs      []int
}

func (e *MessagesDeletedEvent) Type() EventType { return EventTypeMessagesDeleted }
func (e *MessagesDeletedEvent) Timestamp() time.Time { return e.At }

// CallbackOrigin identifies whether the callback originated from a normal message or an inline message.
type CallbackOrigin int

const (
	CallbackOriginMessage CallbackOrigin = iota
	CallbackOriginInline
)

// CallbackTarget models the Telegram target for a callback query.
// For normal messages it carries Peer + MessageID.
// For inline messages it carries InlineID.
// ChatInstance is preserved for both (Telegram's chat_instance).
type CallbackTarget struct {
	Origin       CallbackOrigin
	Peer         tg.InputPeerClass
	MessageID    int
	InlineID     tg.InputBotInlineMessageIDClass
	ChatInstance int64
}

// IsInline reports whether this callback target represents an inline message.
func (t CallbackTarget) IsInline() bool {
	return t.InlineID != nil || t.Origin == CallbackOriginInline
}

// CallbackQueryEvent is the domain event for both normal and inline callback queries.
// ChatID and MsgID are deprecated: use Target.Peer / Target.MessageID or Target.InlineID.
// They are kept for backward compatibility (C) and populated from Target.
type CallbackQueryEvent struct {
	At   time.Time
	Data []byte

	QueryID int64
	UserID  int64

	// Deprecated: use Target.Peer / Target.MessageID.
	ChatID int64
	MsgID  int

	Origin       CallbackOrigin
	Target       CallbackTarget
	ChatInstance int64
}

func (e *CallbackQueryEvent) Type() EventType { return EventTypeCallbackQuery }
func (e *CallbackQueryEvent) Timestamp() time.Time { return e.At }

// IsInline returns true when the callback originated from an inline message,
// using Target.IsInline() as the single source of truth.
func (e *CallbackQueryEvent) IsInline() bool {
	if e == nil {
		return false
	}
	return e.Target.IsInline() || e.Origin == CallbackOriginInline
}

type ReactionUpdatedEvent struct{ At time.Time; MsgID int; ChatID int64; Reaction string }
func (e *ReactionUpdatedEvent) Type() EventType { return EventTypeReactionUpdated }
func (e *ReactionUpdatedEvent) Timestamp() time.Time { return e.At }

// InlineResultChosenEvent is observational feedback when an inline result is chosen/sent.
type InlineResultChosenEvent struct {
	At       time.Time
	UserID   int64
	Query    string
	ResultID string
	InlineID tg.InputBotInlineMessageIDClass
}

func (e *InlineResultChosenEvent) Type() EventType { return EventTypeInlineChosen }
func (e *InlineResultChosenEvent) Timestamp() time.Time { return e.At }

// AdminActionEvent represents an administrative moderation action performed by the bot/user.
type AdminActionEvent struct {
	At         time.Time
	Action     string
	ActorID    int64
	TargetID   int64
	TargetName string
	ChatID     int64
	ChatTitle  string
	Reason     string
	Success    bool
	Error      string
}

func (e *AdminActionEvent) Type() EventType { return EventTypeAdminAction }
func (e *AdminActionEvent) Timestamp() time.Time { return e.At }

// PMPermitEvent represents a private message access control decision or state transition.
type PMPermitEvent struct {
	At         time.Time
	Action     string // "approve", "disapprove", "block", "unblock", "warn", "auto_approve"
	UserID     int64
	TargetName string
	WarnCount  int
	Reason     string
	Success    bool
	Error      string
}

func (e *PMPermitEvent) Type() EventType { return EventTypePMPermit }
func (e *PMPermitEvent) Timestamp() time.Time { return e.At }


type EventHandler func(event Event)

type eventJob struct{ handler EventHandler; event Event }

const (
	eventQueueSize = 1024
	eventWorkers   = 8
)

type EventBusStats struct {
	Published int64
	Delivered int64
	Dropped   int64
	Panics    int64
}

type EventBus struct {
	mu             sync.RWMutex
	subscribers    map[EventType]map[uint64]EventHandler
	nextID         uint64
	queue          chan eventJob
	workers        sync.WaitGroup
	closed         bool
	publishedCount atomic.Int64
	deliveredCount atomic.Int64
	droppedCount   atomic.Int64
	panicCount     atomic.Int64
}

func NewEventBus() *EventBus {
	b := &EventBus{subscribers: make(map[EventType]map[uint64]EventHandler), queue: make(chan eventJob, eventQueueSize)}
	b.workers.Add(eventWorkers)
	for i := 0; i < eventWorkers; i++ {
		go b.worker()
	}
	return b
}

// Stats returns a snapshot of the event bus counters.
func (b *EventBus) Stats() EventBusStats {
	return EventBusStats{
		Published: b.publishedCount.Load(),
		Delivered: b.deliveredCount.Load(),
		Dropped:   b.droppedCount.Load(),
		Panics:    b.panicCount.Load(),
	}
}

func (b *EventBus) worker() {
	for job := range b.queue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					b.panicCount.Add(1)
				}
			}()
			job.handler(job.event)
			b.deliveredCount.Add(1)
		}()
	}
	b.workers.Done()
}

// Subscribe adds a handler. After Close, subscriptions are rejected with a
// no-op unsubscribe function so no goroutine can be attached to a dead bus.
func (b *EventBus) Subscribe(t EventType, handler EventHandler) func() {
	if handler == nil {
		return func() {}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return func() {}
	}
	if b.subscribers[t] == nil {
		b.subscribers[t] = make(map[uint64]EventHandler)
	}
	b.nextID++
	id := b.nextID
	b.subscribers[t][id] = handler
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if m := b.subscribers[t]; m != nil {
			delete(m, id)
			if len(m) == 0 {
				delete(b.subscribers, t)
			}
		}
	}
}

// Publish is intentionally non-blocking. Each subscriber is independently
// queued; a full queue drops only that subscriber's event instead of starving
// later subscribers. Snapshot is taken under read lock to keep hold time short.
func (b *EventBus) Publish(event Event) {
	if event == nil {
		return
	}
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	m := b.subscribers[event.Type()]
	if len(m) == 0 {
		b.mu.RUnlock()
		return
	}
	handlers := make([]EventHandler, 0, len(m))
	for _, h := range m {
		handlers = append(handlers, h)
	}
	b.mu.RUnlock()
	for _, h := range handlers {
		func(handler EventHandler) {
			defer func() { _ = recover() }()
			select {
			case b.queue <- eventJob{handler: handler, event: event}:
				b.publishedCount.Add(1)
			default:
				// Observational events are best-effort by design.
				b.droppedCount.Add(1)
			}
		}(h)
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
	b.subscribers = make(map[EventType]map[uint64]EventHandler)
	close(b.queue)
	b.mu.Unlock()
	b.workers.Wait()
	return nil
}
