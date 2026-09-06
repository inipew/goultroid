package core_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func TestEventBus_SubscribeAndPublish(t *testing.T) {
	bus := core.NewEventBus()
	defer bus.Close()
	var received atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	bus.Subscribe(core.EventTypeMessageCreated, func(e core.Event) { defer wg.Done(); received.Add(1) })
	bus.Publish(&core.MessageCreatedEvent{At: time.Now(), ChatID: 42})
	done := make(chan struct{}); go func() { wg.Wait(); close(done) }()
	select { case <-done: case <-time.After(2 * time.Second): t.Fatal("event handler was not called within timeout") }
	if received.Load() != 1 { t.Errorf("expected 1 call, got %d", received.Load()) }
}

func TestEventBus_MultipleHandlers(t *testing.T) {
	bus := core.NewEventBus(); defer bus.Close()
	var count atomic.Int32; var wg sync.WaitGroup; wg.Add(3)
	for range 3 { bus.Subscribe(core.EventTypeMessageEdited, func(core.Event) { defer wg.Done(); count.Add(1) }) }
	bus.Publish(&core.MessageEditedEvent{At: time.Now(), MsgID: 1, ChatID: 99})
	done := make(chan struct{}); go func() { wg.Wait(); close(done) }()
	select { case <-done: case <-time.After(2 * time.Second): t.Fatal("timed out waiting for handlers") }
	if count.Load() != 3 { t.Errorf("expected 3 handler calls, got %d", count.Load()) }
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := core.NewEventBus(); defer bus.Close()
	var count atomic.Int32
	unsub := bus.Subscribe(core.EventTypeMessagesDeleted, func(core.Event) { count.Add(1) })
	unsub()
	bus.Publish(&core.MessagesDeletedEvent{At: time.Now(), ChatID: 5, MsgIDs: []int{1, 2}})
	time.Sleep(50 * time.Millisecond)
	if count.Load() != 0 { t.Errorf("expected 0 calls after unsubscribe, got %d", count.Load()) }
}

func TestEventBus_NilHandlerIgnored(t *testing.T) {
	bus := core.NewEventBus(); defer bus.Close()
	bus.Subscribe(core.EventTypeMessageCreated, nil)
	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
}

func TestEventBus_NilEventIgnored(t *testing.T) {
	bus := core.NewEventBus(); defer bus.Close()
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { t.Error("handler called with nil event") })
	bus.Publish(nil)
}

func TestEventBus_WrongTypeNotDelivered(t *testing.T) {
	bus := core.NewEventBus(); defer bus.Close()
	var count atomic.Int32
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { count.Add(1) })
	bus.Publish(&core.MessageEditedEvent{At: time.Now()})
	time.Sleep(50 * time.Millisecond)
	if count.Load() != 0 { t.Errorf("handler called for wrong type: got %d calls", count.Load()) }
}

func TestEventBus_CallbackQueryEvent(t *testing.T) {
	bus := core.NewEventBus(); defer bus.Close()
	var wg sync.WaitGroup; wg.Add(1)
	var received *core.CallbackQueryEvent
	bus.Subscribe(core.EventTypeCallbackQuery, func(e core.Event) { defer wg.Done(); if cb, ok := e.(*core.CallbackQueryEvent); ok { received = cb } })
	now := time.Now()
	bus.Publish(&core.CallbackQueryEvent{At: now, QueryID: 12345, UserID: 999, ChatID: -1001234567, MsgID: 42, Data: []byte("btn_click")})
	done := make(chan struct{}); go func() { wg.Wait(); close(done) }()
	select { case <-done: case <-time.After(2 * time.Second): t.Fatal("timed out waiting for callback query handler") }
	if received == nil { t.Fatal("expected non-nil CallbackQueryEvent") }
	if received.QueryID != 12345 || received.UserID != 999 || received.ChatID != -1001234567 || received.MsgID != 42 || string(received.Data) != "btn_click" { t.Errorf("received payload mismatch: %+v", received) }
	if !received.Timestamp().Equal(now) { t.Errorf("timestamp mismatch: got %v, want %v", received.Timestamp(), now) }
}

func TestEventBus_CloseDrainsQueuedEvents(t *testing.T) {
	bus := core.NewEventBus()
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { close(started); <-release; calls.Add(1) })
	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
	select { case <-started: case <-time.After(time.Second): t.Fatal("handler did not start") }
	go func() { time.Sleep(20 * time.Millisecond); close(release) }()
	if err := bus.Close(); err != nil { t.Fatalf("Close() error: %v", err) }
	if calls.Load() != 1 { t.Fatalf("expected queued handler to finish before Close returned, got %d", calls.Load()) }
}

func TestEventBus_CloseIdempotentAndRejectsNewWork(t *testing.T) {
	bus := core.NewEventBus()
	var calls atomic.Int32
	unsub := bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { calls.Add(1) })
	if err := bus.Close(); err != nil { t.Fatalf("first Close() error: %v", err) }
	if err := bus.Close(); err != nil { t.Fatalf("second Close() error: %v", err) }
	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
	unsub()
	if calls.Load() != 0 { t.Fatalf("event published after Close reached handler") }
}

func TestEventBus_PublishDoesNotStarveLaterSubscribersWhenQueueFills(t *testing.T) {
	bus := core.NewEventBus(); defer bus.Close()
	block := make(chan struct{})
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { <-block })
	var later atomic.Int32
	bus.Subscribe(core.EventTypeMessageCreated, func(core.Event) { later.Add(1) })
	for i := 0; i < 1200; i++ { bus.Publish(&core.MessageCreatedEvent{At: time.Now()}) }
	close(block)
	deadline := time.After(2 * time.Second)
	for later.Load() == 0 { select { case <-deadline: t.Fatal("later subscriber was starved"); default: time.Sleep(time.Millisecond) } }
}

func TestEventBus_Stats(t *testing.T) {
	bus := core.NewEventBus()
	defer bus.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	bus.Subscribe(core.EventTypeMessageCreated, func(e core.Event) {
		defer wg.Done()
	})
	bus.Subscribe(core.EventTypeMessageCreated, func(e core.Event) {
		defer wg.Done()
		panic("expected test panic")
	})

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
