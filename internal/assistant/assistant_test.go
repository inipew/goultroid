package assistant_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"go.uber.org/zap"
)

func TestBridge(t *testing.T) {
	b := assistant.NewBridge()
	ctx := context.Background()

	var receivedCount int32
	unsub := b.Subscribe(func(ctx context.Context, e assistant.Event) error {
		if e.Title == "Test Event" {
			atomic.AddInt32(&receivedCount, 1)
		}
		return nil
	})

	b.Dispatch(ctx, assistant.Event{
		Type:    assistant.EventNotification,
		Title:   "Test Event",
		Message: "Hello from bridge",
	})

	// Wait briefly for asynchronous dispatch
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&receivedCount) != 1 {
		t.Errorf("expected 1 event received, got %d", receivedCount)
	}

	// Test unsubscribe
	unsub()
	b.Dispatch(ctx, assistant.Event{
		Type:    assistant.EventNotification,
		Title:   "Test Event",
		Message: "Should not be received",
	})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&receivedCount) != 1 {
		t.Errorf("expected receivedCount to remain 1 after unsubscribe, got %d", receivedCount)
	}
}

func TestBotClient_Initialization(t *testing.T) {
	client := assistant.NewBotClient(1234, "hash", "", zap.NewNop())

	if client.IsRunning() {
		t.Errorf("expected client to not be running initially")
	}
	if client.Username() != "" {
		t.Errorf("expected empty username initially, got %s", client.Username())
	}

	// Should fail to start without bot token
	ctx := context.Background()
	err := client.Start(ctx)
	if !errors.Is(err, assistant.ErrBotTokenRequired) {
		t.Errorf("expected ErrBotTokenRequired, got %v", err)
	}

	// Test setters
	cbStore := callback.NewStateStore()
	cbRouter := callback.NewRouter(zap.NewNop(), cbStore)
	client.SetCallbackRouter(cbRouter)

	inlineEngine := inline.NewEngine(inline.NewRegistry(), zap.NewNop())
	client.SetInlineEngine(inlineEngine)

	bridge := assistant.NewBridge()
	client.SetBridge(bridge)
	if client.Bridge() != bridge {
		t.Errorf("expected set bridge to match")
	}

	// Test Stop on unstarted client
	if err := client.Stop(ctx); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}
