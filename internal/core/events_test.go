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

	var received atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)

	bus.Subscribe(core.EventTypeMessageCreated, func(e core.Event) {
		defer wg.Done()
		received.Add(1)
		if e.Type() != core.EventTypeMessageCreated {
			t.Errorf("wrong event type: got %q", e.Type())
		}
		ts := e.Timestamp()
		if ts.IsZero() {
			t.Error("expected non-zero timestamp")
		}
	})

	ev := &core.MessageCreatedEvent{
		At:     time.Now(),
		ChatID: 42,
	}
	bus.Publish(ev)

	// Wait with timeout for async delivery.
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
	bus := core.NewEventBus()

	var count atomic.Int32
	var wg sync.WaitGroup
	wg.Add(3)

	for range 3 {
		bus.Subscribe(core.EventTypeMessageEdited, func(e core.Event) {
			defer wg.Done()
			count.Add(1)
		})
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
	bus := core.NewEventBus()

	var count atomic.Int32
	unsub := bus.Subscribe(core.EventTypeMessagesDeleted, func(e core.Event) {
		count.Add(1)
	})

	// Unsubscribe before publishing
	unsub()

	bus.Publish(&core.MessagesDeletedEvent{At: time.Now(), ChatID: 5, MsgIDs: []int{1, 2}})
	time.Sleep(100 * time.Millisecond)

	if count.Load() != 0 {
		t.Errorf("expected 0 calls after unsubscribe, got %d", count.Load())
	}
}

func TestEventBus_NilHandlerIgnored(t *testing.T) {
	bus := core.NewEventBus()
	// Should not panic.
	bus.Subscribe(core.EventTypeMessageCreated, nil)
	bus.Publish(&core.MessageCreatedEvent{At: time.Now()})
	time.Sleep(50 * time.Millisecond)
}

func TestEventBus_NilEventIgnored(t *testing.T) {
	bus := core.NewEventBus()
	bus.Subscribe(core.EventTypeMessageCreated, func(e core.Event) {
		t.Error("handler called with nil event")
	})
	// Should not panic.
	bus.Publish(nil)
	time.Sleep(50 * time.Millisecond)
}

func TestEventBus_WrongTypeNotDelivered(t *testing.T) {
	bus := core.NewEventBus()

	var count atomic.Int32
	bus.Subscribe(core.EventTypeMessageCreated, func(e core.Event) {
		count.Add(1)
	})

	// Publish a different type
	bus.Publish(&core.MessageEditedEvent{At: time.Now()})
	time.Sleep(100 * time.Millisecond)

	if count.Load() != 0 {
		t.Errorf("handler called for wrong type: got %d calls", count.Load())
	}
}

func TestEventBus_CallbackQueryEvent(t *testing.T) {
	bus := core.NewEventBus()

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
	ev := &core.CallbackQueryEvent{
		At:      now,
		QueryID: 12345,
		UserID:  999,
		ChatID:  -1001234567,
		MsgID:   42,
		Data:    []byte("btn_click"),
	}
	bus.Publish(ev)

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
	if received.Type() != core.EventTypeCallbackQuery {
		t.Errorf("expected EventTypeCallbackQuery, got %s", received.Type())
	}
	if !received.Timestamp().Equal(now) {
		t.Errorf("timestamp mismatch: got %v, want %v", received.Timestamp(), now)
	}
}
