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

// EventPriority represents the relative urgency of an event.
type EventPriority int

const (
	PriorityCritical EventPriority = 0
	PriorityHigh     EventPriority = 1
	PriorityNormal   EventPriority = 2
	PriorityLow      EventPriority = 3
)

func (p EventPriority) String() string {
	switch p {
	case PriorityCritical:
		return "critical"
	case PriorityHigh:
		return "high"
	case PriorityNormal:
		return "normal"
	case PriorityLow:
		return "low"
	default:
		return "normal"
	}
}

// PrioritizedEvent is an optional interface events can implement to declare dispatch priority.
type PrioritizedEvent interface {
	Priority() EventPriority
}

// OrderedEvent is an optional interface events can implement to declare per-source ordering.
// Events with the same non-empty OrderingKey are guaranteed to be processed in chronological order.
type OrderedEvent interface {
	OrderingKey() string
}

type prioritizedEventWrapper struct {
	Event
	priority EventPriority
}

func (w *prioritizedEventWrapper) Priority() EventPriority {
	return w.priority
}

func (w *prioritizedEventWrapper) OrderingKey() string {
	if oe, ok := w.Event.(OrderedEvent); ok {
		return oe.OrderingKey()
	}
	return ""
}

// WithPriority wraps an event with an explicit priority.
func WithPriority(ev Event, p EventPriority) Event {
	if ev == nil {
		return nil
	}
	return &prioritizedEventWrapper{Event: ev, priority: p}
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
func (e *MessageCreatedEvent) OrderingKey() string {
	if e != nil && e.ChatID != 0 {
		return fmt.Sprintf("chat:%d", e.ChatID)
	}
	return ""
}

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
func (e *MessageEditedEvent) OrderingKey() string {
	if e != nil && e.ChatID != 0 {
		return fmt.Sprintf("chat:%d", e.ChatID)
	}
	return ""
}

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
func (e *MessagesDeletedEvent) OrderingKey() string {
	if e != nil && e.ChatID != 0 {
		return fmt.Sprintf("chat:%d", e.ChatID)
	}
	return ""
}

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
func (e *CallbackQueryEvent) OrderingKey() string {
	if e != nil && e.ChatID != 0 {
		return fmt.Sprintf("chat:%d", e.ChatID)
	}
	return ""
}
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
func (e *ReactionUpdatedEvent) OrderingKey() string {
	if e != nil && e.ChatID != 0 {
		return fmt.Sprintf("chat:%d", e.ChatID)
	}
	return ""
}

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

func (e *AdminActionEvent) Type() EventType         { return EventTypeAdminAction }
func (e *AdminActionEvent) Timestamp() time.Time    { return e.At }
func (e *AdminActionEvent) Meta() EventMeta         { return e.MetaData }
func (e *AdminActionEvent) Priority() EventPriority { return PriorityHigh }
func (e *AdminActionEvent) OrderingKey() string {
	if e != nil && e.ChatID != 0 {
		return fmt.Sprintf("chat:%d", e.ChatID)
	}
	return ""
}

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
	minPriority    EventPriority
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
	priority   EventPriority
}

// SubscribeOptions specifies options when registering an event handler.
type SubscribeOptions struct {
	Owner       string
	Timeout     time.Duration
	MinPriority EventPriority
}

// EventMiddleware intercepts event execution before handler invocation.
type EventMiddleware func(next ContextEventHandler) ContextEventHandler

const (
	eventQueueSize    = 1024
	priorityQueueSize = 256
	eventWorkers      = 8
	orderedPartitions = 4
)

func partitionIndex(key string, n int) int {
	if n <= 1 {
		return 0
	}
	var h uint32
	for i := 0; i < len(key); i++ {
		h = 31*h + uint32(key[i])
	}
	return int(h % uint32(n))
}

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
	mu            sync.RWMutex
	subscribers   map[EventType]map[uint64]eventSubscriber
	nextID        uint64
	queue         chan eventJob
	queueCritical chan eventJob
	queueHigh     chan eventJob
	queueLow      chan eventJob
	orderedQueues [orderedPartitions]chan eventJob
	middlewares   []EventMiddleware
	stop          chan struct{}
	workers       sync.WaitGroup
	closed        bool
	started       bool

	dlqMu sync.RWMutex
	dlq   []DeadLetter

	publishedCount atomic.Int64
	deliveredCount atomic.Int64
	droppedCount   atomic.Int64
	panicCount     atomic.Int64
}

// NewEventBus is a pure constructor. It does not spawn goroutines.
func NewEventBus() *EventBus {
	b := &EventBus{
		subscribers:   make(map[EventType]map[uint64]eventSubscriber),
		queue:         make(chan eventJob, eventQueueSize),
		queueCritical: make(chan eventJob, priorityQueueSize),
		queueHigh:     make(chan eventJob, priorityQueueSize),
		queueLow:      make(chan eventJob, priorityQueueSize),
		stop:          make(chan struct{}),
	}
	for i := 0; i < orderedPartitions; i++ {
		b.orderedQueues[i] = make(chan eventJob, priorityQueueSize)
	}
	return b
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
	b.workers.Add(eventWorkers + orderedPartitions)
	for i := 0; i < eventWorkers; i++ {
		go b.worker()
	}
	for i := 0; i < orderedPartitions; i++ {
		go b.orderedWorker(b.orderedQueues[i])
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
	depth := len(b.queue) + len(b.queueCritical) + len(b.queueHigh) + len(b.queueLow)
	capSum := cap(b.queue) + cap(b.queueCritical) + cap(b.queueHigh) + cap(b.queueLow)
	for i := 0; i < orderedPartitions; i++ {
		depth += len(b.orderedQueues[i])
		capSum += cap(b.orderedQueues[i])
	}
	return EventBusStats{
		Published:     b.publishedCount.Load(),
		Delivered:     b.deliveredCount.Load(),
		Dropped:       b.droppedCount.Load(),
		Panics:        b.panicCount.Load(),
		QueueDepth:    depth,
		QueueCapacity: capSum,
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

// Use attaches event middleware into the dispatch pipeline.
func (b *EventBus) Use(mw ...EventMiddleware) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.middlewares = append(b.middlewares, mw...)
}

func (b *EventBus) worker() {
	defer b.workers.Done()
	for {
		// Non-blocking priority drain checks
		select {
		case job := <-b.queueCritical:
			b.runJob(job)
			continue
		default:
		}

		select {
		case job := <-b.queueCritical:
			b.runJob(job)
			continue
		case job := <-b.queueHigh:
			b.runJob(job)
			continue
		default:
		}

		// Blocking priority select
		select {
		case job := <-b.queueCritical:
			b.runJob(job)
		case job := <-b.queueHigh:
			b.runJob(job)
		case job := <-b.queue:
			b.runJob(job)
		case job := <-b.queueLow:
			b.runJob(job)
		case <-b.stop:
			b.drainQueues()
			return
		}
	}
}

func (b *EventBus) orderedWorker(ch <-chan eventJob) {
	defer b.workers.Done()
	for {
		select {
		case job := <-ch:
			b.runJob(job)
		case <-b.stop:
			for {
				select {
				case job := <-ch:
					b.runJob(job)
				default:
					return
				}
			}
		}
	}
}

func (b *EventBus) drainQueues() {
	for {
		select {
		case job := <-b.queueCritical:
			b.runJob(job)
		case job := <-b.queueHigh:
			b.runJob(job)
		case job := <-b.queue:
			b.runJob(job)
		case job := <-b.queueLow:
			b.runJob(job)
		default:
			return
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

	b.mu.RLock()
	mws := make([]EventMiddleware, len(b.middlewares))
	copy(mws, b.middlewares)
	b.mu.RUnlock()

	var handler ContextEventHandler
	if job.subscriber.contextHandler != nil {
		handler = job.subscriber.contextHandler
	} else if job.subscriber.handler != nil {
		h := job.subscriber.handler
		handler = func(c context.Context, ev Event) error {
			h(ev)
			return nil
		}
	}

	if handler == nil {
		return
	}

	for i := len(mws) - 1; i >= 0; i-- {
		handler = mws[i](handler)
	}

	err := handler(ctx, job.event)
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
	return b.SubscribeWithOptions(t, func(ctx context.Context, event Event) error {
		handler(event)
		return nil
	}, SubscribeOptions{
		Owner:       owner,
		MinPriority: PriorityLow,
	})
}

// SubscribeContextHandler registers a context-aware handler with an ownership label and timeout.
func (b *EventBus) SubscribeContextHandler(owner string, t EventType, handler ContextEventHandler, timeout ...time.Duration) *Subscription {
	var to time.Duration
	if len(timeout) > 0 {
		to = timeout[0]
	}
	return b.SubscribeWithOptions(t, handler, SubscribeOptions{
		Owner:       owner,
		Timeout:     to,
		MinPriority: PriorityLow,
	})
}

// SubscribeWithOptions registers a context-aware handler with custom options (ownership, timeout, min priority).
func (b *EventBus) SubscribeWithOptions(t EventType, handler ContextEventHandler, opts SubscribeOptions) *Subscription {
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
	minPrio := opts.MinPriority
	if minPrio < PriorityCritical || minPrio > PriorityLow {
		minPrio = PriorityLow
	}
	b.subscribers[t][id] = eventSubscriber{
		owner:          opts.Owner,
		contextHandler: handler,
		timeout:        opts.Timeout,
		minPriority:    minPrio,
	}
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

	prio := PriorityNormal
	if pe, ok := event.(PrioritizedEvent); ok {
		prio = pe.Priority()
	}

	var ordKey string
	if oe, ok := event.(OrderedEvent); ok {
		ordKey = oe.OrderingKey()
	}

	for _, subscriber := range m {
		if prio > subscriber.minPriority {
			continue
		}

		job := eventJob{subscriber: subscriber, event: event, priority: prio}
		if ordKey != "" {
			idx := partitionIndex(ordKey, orderedPartitions)
			select {
			case b.orderedQueues[idx] <- job:
				b.publishedCount.Add(1)
			default:
				b.droppedCount.Add(1)
			}
			continue
		}

		var targetChan chan eventJob
		switch prio {
		case PriorityCritical:
			targetChan = b.queueCritical
		case PriorityHigh:
			targetChan = b.queueHigh
		case PriorityLow:
			targetChan = b.queueLow
		default:
			targetChan = b.queue
		}

		select {
		case targetChan <- job:
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

	prio := PriorityNormal
	if pe, ok := event.(PrioritizedEvent); ok {
		prio = pe.Priority()
	}

	mws := make([]EventMiddleware, len(b.middlewares))
	copy(mws, b.middlewares)

	for _, subscriber := range m {
		if prio > subscriber.minPriority {
			continue
		}

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

			var handler ContextEventHandler
			if subscriber.contextHandler != nil {
				handler = subscriber.contextHandler
			} else if subscriber.handler != nil {
				h := subscriber.handler
				handler = func(c context.Context, ev Event) error {
					h(ev)
					return nil
				}
			}

			if handler != nil {
				for i := len(mws) - 1; i >= 0; i-- {
					handler = mws[i](handler)
				}
				handlerErr = handler(ctx, event)
				if handlerErr != nil {
					b.recordDLQ(event, subscriber.owner, handlerErr)
				} else {
					b.deliveredCount.Add(1)
				}
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
