package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/runtime"
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

func (t CallbackTarget) IsInline() bool {
	return t.InlineID != nil || t.Origin == CallbackOriginInline
}

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

// ContextEventHandler defines the context-aware callback for event consumption.
// It receives a bounded context and returns an error if delivery or handling failed.
type ContextEventHandler func(ctx context.Context, event Event) error

type EventHandler func(event Event)

// DeadLetter represents a failed event dispatch attempt.
type DeadLetter struct {
	Event    Event     `json:"event"`
	Owner    string    `json:"owner"`
	Error    string    `json:"error"`
	FailedAt time.Time `json:"failed_at"`
}

type eventSubscriber struct {
	owner          string
	handler        EventHandler
	contextHandler ContextEventHandler
	timeout        time.Duration
}

// Subscription is an owned EventBus registration. Close is idempotent and
// safe to call during EventBus shutdown.
type Subscription struct {
	bus       *EventBus
	eventType EventType
	id        uint64
	once      sync.Once
}

func (s *Subscription) Close() {
	if s == nil || s.bus == nil {
		return
	}
	s.once.Do(func() {
		s.bus.mu.Lock()
		defer s.bus.mu.Unlock()
		if m := s.bus.subscribers[s.eventType]; m != nil {
			delete(m, s.id)
			if len(m) == 0 {
				delete(s.bus.subscribers, s.eventType)
			}
		}
	})
}

type eventJob struct {
	subscriber eventSubscriber
	event      Event
}

const (
	eventQueueSize = 1024
	eventWorkers   = 8
)

var (
	ErrEventBusClosed     = errors.New("event bus closed")
	ErrEventBusNotStarted = errors.New("event bus not started")
)

type EventBusStats struct {
	Published     int64
	Delivered     int64
	Dropped       int64
	Panics        int64
	QueueDepth    int
	QueueCapacity int
}

// Ensure EventBus implements runtime.Component.
var _ runtime.Component = (*EventBus)(nil)

type EventBus struct {
	mu          sync.RWMutex
	subscribers map[EventType]map[uint64]eventSubscriber
	nextID      uint64
	queue       chan eventJob
	stop        chan struct{}
	workers     sync.WaitGroup
	closed      bool
	started     bool

	dlqMu sync.RWMutex
	dlq   []DeadLetter

	publishedCount atomic.Int64
	deliveredCount atomic.Int64
	droppedCount   atomic.Int64
	panicCount     atomic.Int64
}

// NewEventBus is a pure constructor. It does not spawn goroutines.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[EventType]map[uint64]eventSubscriber),
		queue:       make(chan eventJob, eventQueueSize),
		stop:        make(chan struct{}),
	}
}

// Start launches workers exactly once. Cancellation of ctx requests a graceful
// Close, which drains already accepted jobs instead of abandoning the queue.
func (b *EventBus) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrEventBusClosed
	}
	if b.started {
		b.mu.Unlock()
		return nil
	}
	b.started = true
	b.workers.Add(eventWorkers)
	for i := 0; i < eventWorkers; i++ {
		go b.worker()
	}
	b.mu.Unlock()

	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			_ = b.Close()
		}()
	}
	return nil
}

func (b *EventBus) Stats() EventBusStats {
	return EventBusStats{
		Published:     b.publishedCount.Load(),
		Delivered:     b.deliveredCount.Load(),
		Dropped:       b.droppedCount.Load(),
		Panics:        b.panicCount.Load(),
		QueueDepth:    len(b.queue),
		QueueCapacity: cap(b.queue),
	}
}

// LifecycleState reports the EventBus lifecycle without exposing mutable
// internals. Possible values are new, started, and closed.
func (b *EventBus) LifecycleState() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return "closed"
	}
	if b.started {
		return "started"
	}
	return "new"
}

func (b *EventBus) worker() {
	defer b.workers.Done()
	for {
		select {
		case job := <-b.queue:
			b.runJob(job)
		case <-b.stop:
			for {
				select {
				case job := <-b.queue:
					b.runJob(job)
				default:
					return
				}
			}
		}
	}
}

func (b *EventBus) runJob(job eventJob) {
	defer func() {
		if r := recover(); r != nil {
			b.panicCount.Add(1)
			b.recordDLQ(job.event, job.subscriber.owner, fmt.Errorf("panic in event handler: %v", r))
		}
	}()

	timeout := job.subscriber.timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var err error
	if job.subscriber.contextHandler != nil {
		err = job.subscriber.contextHandler(ctx, job.event)
	} else if job.subscriber.handler != nil {
		job.subscriber.handler(job.event)
	}

	if err != nil {
		b.recordDLQ(job.event, job.subscriber.owner, err)
	} else {
		b.deliveredCount.Add(1)
	}
}

func (b *EventBus) recordDLQ(ev Event, owner string, err error) {
	b.dlqMu.Lock()
	defer b.dlqMu.Unlock()

	entry := DeadLetter{
		Event:    ev,
		Owner:    owner,
		Error:    err.Error(),
		FailedAt: time.Now().UTC(),
	}
	if len(b.dlq) >= 500 {
		b.dlq = b.dlq[1:]
	}
	b.dlq = append(b.dlq, entry)
}

// DLQ returns a snapshot of recorded dead letter events.
func (b *EventBus) DLQ() []DeadLetter {
	b.dlqMu.RLock()
	defer b.dlqMu.RUnlock()

	result := make([]DeadLetter, len(b.dlq))
	copy(result, b.dlq)
	return result
}

// ClearDLQ clears the recorded dead letter entries.
func (b *EventBus) ClearDLQ() {
	b.dlqMu.Lock()
	defer b.dlqMu.Unlock()
	b.dlq = nil
}

func (b *EventBus) Subscribe(t EventType, handler EventHandler) func() {
	sub := b.SubscribeOwned("", t, handler)
	if sub == nil {
		return func() {}
	}
	return sub.Close
}

// SubscribeOwned registers a handler with an ownership label for diagnostics.
// The returned subscription should be closed by the owning plugin scope.
func (b *EventBus) SubscribeOwned(owner string, t EventType, handler EventHandler) *Subscription {
	if handler == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	if b.subscribers[t] == nil {
		b.subscribers[t] = make(map[uint64]eventSubscriber)
	}
	b.nextID++
	id := b.nextID
	b.subscribers[t][id] = eventSubscriber{owner: owner, handler: handler}
	return &Subscription{bus: b, eventType: t, id: id}
}

// SubscribeContextHandler registers a context-aware handler with an ownership label and timeout.
func (b *EventBus) SubscribeContextHandler(owner string, t EventType, handler ContextEventHandler, timeout ...time.Duration) *Subscription {
	if handler == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	if b.subscribers[t] == nil {
		b.subscribers[t] = make(map[uint64]eventSubscriber)
	}
	b.nextID++
	id := b.nextID
	var to time.Duration
	if len(timeout) > 0 {
		to = timeout[0]
	}
	b.subscribers[t][id] = eventSubscriber{owner: owner, contextHandler: handler, timeout: to}
	return &Subscription{bus: b, eventType: t, id: id}
}

// SubscribeContextHandlerWithCancel registers a context-aware handler that is cancelled when ctx is done.
func (b *EventBus) SubscribeContextHandlerWithCancel(ctx context.Context, owner string, t EventType, handler ContextEventHandler, timeout ...time.Duration) *Subscription {
	sub := b.SubscribeContextHandler(owner, t, handler, timeout...)
	if sub == nil {
		return nil
	}
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				sub.Close()
			case <-b.stop:
			}
		}()
	}
	return sub
}

// SubscribeContext registers a handler that is automatically cancelled when ctx is done.
func (b *EventBus) SubscribeContext(ctx context.Context, owner string, t EventType, handler EventHandler) *Subscription {
	sub := b.SubscribeOwned(owner, t, handler)
	if sub == nil {
		return nil
	}
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				sub.Close()
			case <-b.stop:
			}
		}()
	}
	return sub
}

// SubscriptionCount reports active subscriptions, optionally filtered by owner.
func (b *EventBus) SubscriptionCount(owner string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	count := 0
	for _, subscribers := range b.subscribers {
		for _, subscriber := range subscribers {
			if owner == "" || subscriber.owner == owner {
				count++
			}
		}
	}
	return count
}

// Publish is best-effort and non-blocking. It serializes queue sends with
// lifecycle changes, so Close can never race a producer into a closed channel.
func (b *EventBus) Publish(event Event) {
	if event == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed || !b.started {
		return
	}
	m := b.subscribers[event.Type()]
	if len(m) == 0 {
		return
	}
	for _, subscriber := range m {
		select {
		case b.queue <- eventJob{subscriber: subscriber, event: event}:
			b.publishedCount.Add(1)
		default:
			b.droppedCount.Add(1)
		}
	}
}

// PublishDurable delivers synchronously and never silently drops. Close waits
// for an in-flight durable publish because it holds the read lock for delivery.
func (b *EventBus) PublishDurable(ctx context.Context, event Event) error {
	if event == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return ErrEventBusClosed
	}
	if !b.started {
		return ErrEventBusNotStarted
	}
	m := b.subscribers[event.Type()]
	if len(m) == 0 {
		return nil
	}
	for _, subscriber := range m {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var handlerErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					b.panicCount.Add(1)
					handlerErr = fmt.Errorf("event handler panic: %v", r)
					b.recordDLQ(event, subscriber.owner, handlerErr)
				}
			}()
			if subscriber.contextHandler != nil {
				handlerErr = subscriber.contextHandler(ctx, event)
				if handlerErr != nil {
					b.recordDLQ(event, subscriber.owner, handlerErr)
				} else {
					b.deliveredCount.Add(1)
				}
			} else if subscriber.handler != nil {
				subscriber.handler(event)
				b.deliveredCount.Add(1)
			}
		}()
		if handlerErr != nil {
			return handlerErr
		}
	}
	return nil
}

// Name returns component name for runtime.Component.
func (b *EventBus) Name() string {
	return "eventbus"
}

// Dependencies returns empty dependencies for runtime.Component.
func (b *EventBus) Dependencies() []string {
	return nil
}

// Stop gracefully closes the event bus.
func (b *EventBus) Stop(ctx context.Context) error {
	return b.Close()
}

// Health evaluates EventBus health.
func (b *EventBus) Health(ctx context.Context) runtime.ComponentHealth {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return runtime.ComponentHealth{
			Status:  runtime.HealthUnhealthy,
			Details: "event bus closed",
		}
	}
	if !b.started {
		return runtime.ComponentHealth{
			Status:  runtime.HealthDegraded,
			Details: "event bus not started",
		}
	}
	if b.droppedCount.Load() > 0 {
		return runtime.ComponentHealth{
			Status:  runtime.HealthDegraded,
			Details: fmt.Sprintf("event bus dropped %d events", b.droppedCount.Load()),
		}
	}
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

// Close is idempotent and drains all accepted asynchronous jobs. The data queue
// is intentionally never closed; stop is the lifecycle broadcast, eliminating
// send/close races.
func (b *EventBus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.workers.Wait()
		return nil
	}
	b.closed = true
	b.subscribers = make(map[EventType]map[uint64]eventSubscriber)
	if b.started {
		close(b.stop)
	}
	b.mu.Unlock()
	b.workers.Wait()
	return nil
}
