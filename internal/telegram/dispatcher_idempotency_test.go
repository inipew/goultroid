package telegram

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/idempotency"
	"go.uber.org/zap"
)

func TestDispatcher_IdempotencyAndEventPublish(t *testing.T) {
	router := core.NewRouter(".")
	perms := core.NewPermissions(12345, nil)
	d := NewDispatcher(router, perms, nil, zap.NewNop())

	bus := core.NewEventBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = bus.Start(ctx)
	defer func() { _ = bus.Close() }()

	d.SetEventBus(bus)

	idemp := idempotency.NewManager(1 * time.Minute)
	defer idemp.Close()
	d.SetIdempotency(idemp)

	var eventsReceived atomic.Int32
	bus.Subscribe(core.EventTypeMessageCreated, func(evt core.Event) {
		eventsReceived.Add(1)
	})

	var interceptorCalls atomic.Int32
	d.AddMessageHandler(func(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error {
		interceptorCalls.Add(1)
		return nil
	})

	msg := &tg.Message{
		ID:      42,
		PeerID:  &tg.PeerChat{ChatID: 999},
		Message: "hello world",
	}

	// First dispatch: should succeed, interceptor called, event published
	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}

	if interceptorCalls.Load() != 1 {
		t.Fatalf("expected 1 interceptor call, got %d", interceptorCalls.Load())
	}

	// Wait for async event bus delivery
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if eventsReceived.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if eventsReceived.Load() != 1 {
		t.Fatalf("expected 1 event received, got %d", eventsReceived.Load())
	}

	// Second dispatch with same msg ID: should be dropped by idempotency manager
	if err := d.dispatch(ctx, tg.Entities{}, msg); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}

	// Interceptor count and event count should NOT increase
	if interceptorCalls.Load() != 1 {
		t.Fatalf("expected duplicate message to be dropped, got %d interceptor calls", interceptorCalls.Load())
	}
}
