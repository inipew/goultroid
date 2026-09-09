package core_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func newStartedEventBus(t *testing.T) *core.EventBus {
	t.Helper()
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func TestEventBus_SubscribeAndPublish(t *testing.T) {
	bus := newStartedEventBus(t)
	var received atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	bus.Subscribe(core.EventTypeMessageCreated, func(e core.Event) { defer wg.Done(); received.Add(1) })
	bus.Publish(&core.MessageCreatedEvent{At: time.Now(), ChatID: 42})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event handler was not called within timeout")
	}
	if received.Load() != 1 {
		t.Errorf("expected 1 call, got %d", received.Load())
	}
}

func TestEventBus_MultipleHandlers(t *testing.T) {
	bus := newStartedEventBus(t)
	var count atomic.Int32
	var wg sync.WaitGroup
	wg.Add(3)
	for range 3 {
		bus.Subscribe(core.EventTypeMessageEdited, func(core.Event) { defer wg.Done(); count.Add(1) })
	}
	bus.Publish(&core.MessageEditedEvent{At: time.Now(), MsgID: 1, ChatID: 99})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handlers")
	}
	if count.Load() != 3 {
		t.Errorf("expected 3 handler calls, got %d", count.Load())
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := newStartedEventBus(t)
	var count atomic.Int32
	unsub := bus.Subscribe(core.EventTypeMessagesDeleted, func(core.Event) { count.Add(1) })
	unsub()
	bus.Publish(&core.MessagesDeletedEvent{At: time.Now(), ChatID: 5, MsgIDs: []int{1, 2}})
	time.Sleep(50 * time.Millisecond)
	if count.Load() != 0 {
		t.Errorf("expected 0 calls after unsubscribe, got %d", count.Load())
	}
}

func TestEventBus_NilHandlerIgnored(t *testing.T) {
	bus := newStartedEventBus(t)
	bus.Subscribe(core.EventTypeMessageCreated, nil)
	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
}

func TestEventBus_NilEventIgnored(t *testing.T) {
	bus := newStartedEventBus(t)
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { t.Error("handler called with nil event") })
	bus.Publish(nil)
}

func TestEventBus_WrongTypeNotDelivered(t *testing.T) {
	bus := newStartedEventBus(t)
	var count atomic.Int32
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { count.Add(1) })
	bus.Publish(&core.MessageEditedEvent{At: time.Now()})
	time.Sleep(50 * time.Millisecond)
	if count.Load() != 0 {
		t.Errorf("handler called for wrong type: got %d calls", count.Load())
	}
}

func TestEventBus_CallbackQueryEvent(t *testing.T) {
	bus := newStartedEventBus(t)
	var wg sync.WaitGroup
	wg.Add(1)
	var received *core.CallbackQueryEvent
	bus.Subscribe(core.EventTypeCallbackQuery, func(e core.Event) {
		defer wg.Done()
		if cb, ok := e.(*core.CallbackQueryEvent); ok {
			received = cb
		}
	})
	now := time.Now()
	bus.Publish(&core.CallbackQueryEvent{At: now, QueryID: 12345, UserID: 999, ChatID: -1001234567, MsgID: 42, Data: []byte("btn_click")})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback query handler")
	}
	if received == nil {
		t.Fatal("expected non-nil CallbackQueryEvent")
	}
	if received.QueryID != 12345 || received.UserID != 999 || received.ChatID != -1001234567 || received.MsgID != 42 || string(received.Data) != "btn_click" {
		t.Errorf("received payload mismatch: %+v", received)
	}
	if !received.Timestamp().Equal(now) {
		t.Errorf("timestamp mismatch: got %v, want %v", received.Timestamp(), now)
	}
}

func TestEventBus_CloseDrainsQueuedEvents(t *testing.T) {
	bus := newStartedEventBus(t)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { close(started); <-release; calls.Add(1) })
	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	go func() { time.Sleep(20 * time.Millisecond); close(release) }()
	if err := bus.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected queued handler to finish before Close returned, got %d", calls.Load())
	}
}

func TestEventBus_CloseIdempotentAndRejectsNewWork(t *testing.T) {
	bus := newStartedEventBus(t)
	var calls atomic.Int32
	unsub := bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { calls.Add(1) })
	if err := bus.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("second Close() error: %v", err)
	}
	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
	unsub()
	if calls.Load() != 0 {
		t.Fatalf("event published after Close reached handler")
	}
}

func TestEventBus_PublishDoesNotStarveLaterSubscribersWhenQueueFills(t *testing.T) {
	bus := newStartedEventBus(t)
	block := make(chan struct{})
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { <-block })
	var later atomic.Int32
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { later.Add(1) })
	for i := 0; i < 1200; i++ {
		bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
	}
	close(block)
	deadline := time.After(2 * time.Second)
	for later.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("later subscriber was starved")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestEventBus_Stats(t *testing.T) {
	bus := newStartedEventBus(t)
	var wg sync.WaitGroup
	wg.Add(2)
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { defer wg.Done() })
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { defer wg.Done(); panic("expected test panic") })
	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
	wg.Wait()
	time.Sleep(10 * time.Millisecond)
	stats := bus.Stats()
	if stats.Published < 2 {
		t.Errorf("expected published >= 2, got %d", stats.Published)
	}
	if stats.Panics != 1 {
		t.Errorf("expected 1 panic, got %d", stats.Panics)
	}
	if stats.Delivered < 1 {
		t.Errorf("expected delivered >= 1, got %d", stats.Delivered)
	}
}

func TestEventBus_OwnedSubscriptionLifecycle(t *testing.T) {
	bus := newStartedEventBus(t)
	sub := bus.SubscribeOwned("plugin:test", core.EventTypeMessageCreated, func(core.Event) {})
	if sub == nil {
		t.Fatal("expected owned subscription")
	}
	if got := bus.SubscriptionCount("plugin:test"); got != 1 {
		t.Fatalf("subscription count = %d, want 1", got)
	}
	sub.Close()
	sub.Close()
	if got := bus.SubscriptionCount("plugin:test"); got != 0 {
		t.Fatalf("subscription count after close = %d, want 0", got)
	}
}

func TestEventBus_ContextHandlerAndTimeout(t *testing.T) {
	bus := newStartedEventBus(t)
	handled := make(chan struct{})

	sub := bus.SubscribeContextHandler("plugin:ctx", core.EventTypeMessageCreated, func(ctx context.Context, e core.Event) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			close(handled)
			return nil
		}
	}, 1*time.Second)
	defer sub.Close()

	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})

	select {
	case <-handled:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for context-aware event handler")
	}
}

func TestEventBus_DeadLetterQueueOnFailure(t *testing.T) {
	bus := newStartedEventBus(t)
	var wg sync.WaitGroup
	wg.Add(1)

	sub := bus.SubscribeContextHandler("plugin:failing", core.EventTypeMessageCreated, func(ctx context.Context, e core.Event) error {
		defer wg.Done()
		return errors.New("synthetic handler error")
	})
	defer sub.Close()

	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
	wg.Wait()
	time.Sleep(20 * time.Millisecond)

	dlq := bus.DLQ()
	if len(dlq) == 0 {
		t.Fatal("expected at least 1 dead letter entry")
	}
	if dlq[len(dlq)-1].Owner != "plugin:failing" {
		t.Errorf("expected owner plugin:failing, got %s", dlq[len(dlq)-1].Owner)
	}
	if dlq[len(dlq)-1].Error != "synthetic handler error" {
		t.Errorf("expected synthetic handler error, got %s", dlq[len(dlq)-1].Error)
	}

	bus.ClearDLQ()
	if len(bus.DLQ()) != 0 {
		t.Errorf("expected empty DLQ after ClearDLQ")
	}
}

func TestEventBus_RuntimeComponent(t *testing.T) {
	bus := core.NewEventBus()
	if bus.Name() != "eventbus" {
		t.Errorf("expected name eventbus, got %s", bus.Name())
	}
	if len(bus.Dependencies()) != 0 {
		t.Errorf("expected empty dependencies, got %v", bus.Dependencies())
	}

	hBefore := bus.Health(context.Background())
	if hBefore.Status != "degraded" {
		t.Errorf("expected degraded before start, got %s", hBefore.Status)
	}

	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	hRunning := bus.Health(context.Background())
	if hRunning.Status != "healthy" {
		t.Errorf("expected healthy while running, got %s", hRunning.Status)
	}

	if err := bus.Stop(context.Background()); err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	hStopped := bus.Health(context.Background())
	if hStopped.Status != "unhealthy" {
		t.Errorf("expected unhealthy after stop, got %s", hStopped.Status)
	}
}

func TestEventBus_PriorityDispatch(t *testing.T) {
	bus := newStartedEventBus(t)

	var highReceived atomic.Int32
	var normalReceived atomic.Int32

	bus.Subscribe(core.EventTypeAdminAction, func(e core.Event) {
		highReceived.Add(1)
	})
	bus.Subscribe(core.EventTypeMessageCreated, func(e core.Event) {
		normalReceived.Add(1)
	})

	// AdminActionEvent implements Priority() == PriorityHigh
	adminEv := &core.AdminActionEvent{At: time.Now(), Action: "ban", ChatID: 10}
	if adminEv.Priority() != core.PriorityHigh {
		t.Fatalf("expected admin action priority high, got %v", adminEv.Priority())
	}
	bus.Publish(adminEv)

	msgEv := &core.MessageCreatedEvent{At: time.Now(), ChatID: 10}
	bus.Publish(msgEv)

	time.Sleep(50 * time.Millisecond)

	if highReceived.Load() != 1 {
		t.Errorf("expected 1 high priority event received, got %d", highReceived.Load())
	}
	if normalReceived.Load() != 1 {
		t.Errorf("expected 1 normal priority event received, got %d", normalReceived.Load())
	}
}

func TestEventBus_OrderedEvents(t *testing.T) {
	bus := newStartedEventBus(t)

	const count = 50
	var mu sync.Mutex
	var processed []int

	var wg sync.WaitGroup
	wg.Add(count)

	bus.SubscribeContextHandler("test", core.EventTypeMessageEdited, func(ctx context.Context, ev core.Event) error {
		defer wg.Done()
		ed, ok := ev.(*core.MessageEditedEvent)
		if !ok {
			return nil
		}
		mu.Lock()
		processed = append(processed, ed.MsgID)
		mu.Unlock()
		return nil
	})

	// Publish 50 message edited events for the same ChatID
	for i := 0; i < count; i++ {
		bus.Publish(&core.MessageEditedEvent{
			At:     time.Now(),
			ChatID: 999, // Same OrderingKey: "chat:999"
			MsgID:  i,
		})
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for ordered events")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(processed) != count {
		t.Fatalf("expected %d events, got %d", count, len(processed))
	}

	// Strictly sequential FIFO order check
	for i := 0; i < count; i++ {
		if processed[i] != i {
			t.Fatalf("ordering violated at index %d: expected msgID %d, got %d (sequence: %v)", i, i, processed[i], processed[:min(10, len(processed))])
		}
	}
}

func TestEventBus_Middleware(t *testing.T) {
	bus := core.NewEventBus()

	var order []string
	var mu sync.Mutex

	mw1 := func(next core.ContextEventHandler) core.ContextEventHandler {
		return func(ctx context.Context, event core.Event) error {
			mu.Lock()
			order = append(order, "mw1_before")
			mu.Unlock()
			err := next(ctx, event)
			mu.Lock()
			order = append(order, "mw1_after")
			mu.Unlock()
			return err
		}
	}

	mw2 := func(next core.ContextEventHandler) core.ContextEventHandler {
		return func(ctx context.Context, event core.Event) error {
			mu.Lock()
			order = append(order, "mw2_before")
			mu.Unlock()
			err := next(ctx, event)
			mu.Lock()
			order = append(order, "mw2_after")
			mu.Unlock()
			return err
		}
	}

	bus.Use(mw1, mw2)
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("start bus: %v", err)
	}
	defer bus.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	bus.SubscribeContextHandler("test", core.EventTypeSettingChanged, func(ctx context.Context, ev core.Event) error {
		defer wg.Done()
		mu.Lock()
		order = append(order, "handler")
		mu.Unlock()
		return nil
	})

	bus.Publish(&core.SettingChangedEvent{At: time.Now(), Key: "theme"})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for middleware handler")
	}

	// Wait briefly for trailing mw1_after
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	expected := []string{"mw1_before", "mw2_before", "handler", "mw2_after", "mw1_after"}
	if len(order) != len(expected) {
		t.Fatalf("expected order %v, got %v", expected, order)
	}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("at index %d: expected %s, got %s", i, v, order[i])
		}
	}
}

func TestEventBus_SubscribeWithOptions_MinPriority(t *testing.T) {
	bus := newStartedEventBus(t)

	var criticalOnly atomic.Int32

	bus.SubscribeWithOptions(core.EventTypeMessageCreated, func(ctx context.Context, ev core.Event) error {
		criticalOnly.Add(1)
		return nil
	}, core.SubscribeOptions{
		Owner:       "critical-listener",
		MinPriority: core.PriorityCritical, // Only receives Critical (0)
	})

	// 1. Normal priority event should be ignored by critical-only subscriber
	normalEv := &core.MessageCreatedEvent{At: time.Now(), ChatID: 1}
	bus.Publish(normalEv)
	time.Sleep(30 * time.Millisecond)
	if criticalOnly.Load() != 0 {
		t.Fatalf("expected 0 calls for normal event, got %d", criticalOnly.Load())
	}

	// 2. Critical priority event should be delivered
	critEv := core.WithPriority(&core.MessageCreatedEvent{At: time.Now(), ChatID: 2}, core.PriorityCritical)
	bus.Publish(critEv)
	time.Sleep(30 * time.Millisecond)
	if criticalOnly.Load() != 1 {
		t.Fatalf("expected 1 call for critical event, got %d", criticalOnly.Load())
	}
}

func TestEventBus_PublishDurableAllowsSubscriberToCloseItself(t *testing.T) {
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	var sub *core.Subscription
	sub = bus.SubscribeContextHandler("self-closing", core.EventTypeSettingChanged, func(context.Context, core.Event) error {
		sub.Close()
		return nil
	})
	done := make(chan error, 1)
	go func() {
		done <- bus.PublishDurable(context.Background(), &core.SettingChangedEvent{At: time.Now(), Key: "x"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("durable subscriber deadlocked while closing itself")
	}
}

func TestEventBus_CloseContextHonorsDeadline(t *testing.T) {
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	bus.SubscribeContextHandler("blocked", core.EventTypeSettingChanged, func(context.Context, core.Event) error {
		close(started)
		<-release
		return nil
	})
	go func() {
		_ = bus.PublishDurable(context.Background(), &core.SettingChangedEvent{At: time.Now(), Key: "x"})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := bus.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext() error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := bus.CloseContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}
