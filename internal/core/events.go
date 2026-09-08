package core

import (
	"context"
	"fmt"
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
	EventTypeSettingChanged  EventType = "setting.changed"
)

type EventMeta struct {
	ID            string
	CorrelationID string
	CausationID   string
}
type Event interface {
	Type() EventType
	Timestamp() time.Time
	Meta() EventMeta
}
type MessageCreatedEvent struct {
	MetaData EventMeta
	At       time.Time
	Message  *Message
	ChatID   int64
	PeerID   interface{}
}

func (e *MessageCreatedEvent) Type() EventType      { return EventTypeMessageCreated }
func (e *MessageCreatedEvent) Timestamp() time.Time { return e.At }
func (e *MessageCreatedEvent) Meta() EventMeta      { return e.MetaData }

type MessageEditedEvent struct {
	MetaData EventMeta
	At       time.Time
	MsgID    int
	ChatID   int64
	Text     string
}

func (e *MessageEditedEvent) Type() EventType      { return EventTypeMessageEdited }
func (e *MessageEditedEvent) Timestamp() time.Time { return e.At }
func (e *MessageEditedEvent) Meta() EventMeta      { return e.MetaData }

type MessagesDeletedEvent struct {
	MetaData    EventMeta
	At          time.Time
	ChatID      int64
	PeerUnknown bool
	MsgIDs      []int
}

func (e *MessagesDeletedEvent) Type() EventType      { return EventTypeMessagesDeleted }
func (e *MessagesDeletedEvent) Timestamp() time.Time { return e.At }
func (e *MessagesDeletedEvent) Meta() EventMeta      { return e.MetaData }

type CallbackOrigin int

const (
	CallbackOriginMessage CallbackOrigin = iota
	CallbackOriginInline
)

type CallbackTarget struct {
	Origin       CallbackOrigin
	Peer         tg.InputPeerClass
	MessageID    int
	InlineID     tg.InputBotInlineMessageIDClass
	ChatInstance int64
}

func (t CallbackTarget) IsInline() bool { return t.InlineID != nil || t.Origin == CallbackOriginInline }

type CallbackQueryEvent struct {
	MetaData     EventMeta
	At           time.Time
	Data         []byte
	QueryID      int64
	UserID       int64
	ChatID       int64
	MsgID        int
	Origin       CallbackOrigin
	Target       CallbackTarget
	ChatInstance int64
}

func (e *CallbackQueryEvent) Type() EventType      { return EventTypeCallbackQuery }
func (e *CallbackQueryEvent) Timestamp() time.Time { return e.At }
func (e *CallbackQueryEvent) Meta() EventMeta      { return e.MetaData }
func (e *CallbackQueryEvent) IsInline() bool {
	return e != nil && (e.Target.IsInline() || e.Origin == CallbackOriginInline)
}

type ReactionUpdatedEvent struct {
	MetaData EventMeta
	At       time.Time
	MsgID    int
	ChatID   int64
	Reaction string
}

func (e *ReactionUpdatedEvent) Type() EventType      { return EventTypeReactionUpdated }
func (e *ReactionUpdatedEvent) Timestamp() time.Time { return e.At }
func (e *ReactionUpdatedEvent) Meta() EventMeta      { return e.MetaData }

type InlineResultChosenEvent struct {
	MetaData EventMeta
	At       time.Time
	UserID   int64
	Query    string
	ResultID string
	InlineID tg.InputBotInlineMessageIDClass
}

func (e *InlineResultChosenEvent) Type() EventType      { return EventTypeInlineChosen }
func (e *InlineResultChosenEvent) Timestamp() time.Time { return e.At }
func (e *InlineResultChosenEvent) Meta() EventMeta      { return e.MetaData }

type AdminActionEvent struct {
	MetaData   EventMeta
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

func (e *AdminActionEvent) Type() EventType      { return EventTypeAdminAction }
func (e *AdminActionEvent) Timestamp() time.Time { return e.At }
func (e *AdminActionEvent) Meta() EventMeta      { return e.MetaData }

type PMPermitEvent struct {
	MetaData   EventMeta
	At         time.Time
	Action     string
	UserID     int64
	TargetName string
	WarnCount  int
	Reason     string
	Success    bool
	Error      string
}

func (e *PMPermitEvent) Type() EventType      { return EventTypePMPermit }
func (e *PMPermitEvent) Timestamp() time.Time { return e.At }
func (e *PMPermitEvent) Meta() EventMeta      { return e.MetaData }

type SettingChangedEvent struct {
	MetaData  EventMeta
	At        time.Time
	ScopeType string
	ScopeID   int64
	Namespace string
	Key       string
	OldVal    string
	NewVal    string
	ChangedBy int64
}

func (e *SettingChangedEvent) Type() EventType      { return EventTypeSettingChanged }
func (e *SettingChangedEvent) Timestamp() time.Time { return e.At }
func (e *SettingChangedEvent) Meta() EventMeta      { return e.MetaData }

type EventHandler func(event Event)
type eventJob struct {
	handler EventHandler
	event   Event
}

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

var (
	ErrEventBusClosed     = fmt.Errorf("event bus closed")
	ErrEventBusNotStarted = fmt.Errorf("event bus not started")
)

type EventBus struct {
	mu             sync.RWMutex
	subscribers    map[EventType]map[uint64]EventHandler
	nextID         uint64
	queue          chan eventJob
	workers        sync.WaitGroup
	closed         bool
	started        bool
	publishedCount atomic.Int64
	deliveredCount atomic.Int64
	droppedCount   atomic.Int64
	panicCount     atomic.Int64
}

func NewEventBus() *EventBus {
	return &EventBus{subscribers: make(map[EventType]map[uint64]EventHandler), queue: make(chan eventJob, eventQueueSize)}
}
func (b *EventBus) startLocked() {
	if b.started {
		return
	}
	b.started = true
	b.workers.Add(eventWorkers)
	for i := 0; i < eventWorkers; i++ {
		go b.worker()
	}
}
func (b *EventBus) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrEventBusClosed
	}
	b.startLocked()
	return nil
}
func (b *EventBus) Stats() EventBusStats {
	return EventBusStats{Published: b.publishedCount.Load(), Delivered: b.deliveredCount.Load(), Dropped: b.droppedCount.Load(), Panics: b.panicCount.Load()}
}
func (b *EventBus) worker() {
	defer b.workers.Done()
	for job := range b.queue {
		func() {
			defer func() {
				if recover() != nil {
					b.panicCount.Add(1)
				}
			}()
			job.handler(job.event)
			b.deliveredCount.Add(1)
		}()
	}
}
func (b *EventBus) Subscribe(t EventType, h EventHandler) func() {
	if h == nil {
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
	b.subscribers[t][id] = h
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

// Publish remains backwards-compatible with old callers by lazily starting the
// worker pool. App should still call Start(ctx) so lifecycle ownership is explicit.
func (b *EventBus) Publish(e Event) {
	if e == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		b.droppedCount.Add(1)
		return
	}
	b.startLocked()
	for _, h := range b.subscribers[e.Type()] {
		select {
		case b.queue <- eventJob{handler: h, event: e}:
			b.publishedCount.Add(1)
		default:
			b.droppedCount.Add(1)
		}
	}
}
func (b *EventBus) PublishDurable(ctx context.Context, e Event) error {
	if e == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrEventBusClosed
	}
	b.startLocked()
	hs := make([]EventHandler, 0, len(b.subscribers[e.Type()]))
	for _, h := range b.subscribers[e.Type()] {
		hs = append(hs, h)
	}
	b.mu.Unlock()
	for _, h := range hs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var he error
		func() {
			defer func() {
				if r := recover(); r != nil {
					b.panicCount.Add(1)
					he = fmt.Errorf("event handler panic: %v", r)
				}
			}()
			h(e)
			b.deliveredCount.Add(1)
		}()
		if he != nil {
			return he
		}
	}
	return nil
}
func (b *EventBus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.subscribers = make(map[EventType]map[uint64]EventHandler)
	if b.started {
		close(b.queue)
	}
	b.mu.Unlock()
	b.workers.Wait()
	return nil
}
