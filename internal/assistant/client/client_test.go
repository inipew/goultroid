package client_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/client"
)

func TestLifecycle(t *testing.T) {
	lc := client.NewLifecycle()
	if lc.State() != client.StateNew {
		t.Fatalf("expected StateNew, got %v", lc.State())
	}

	if !lc.TryStart() {
		t.Fatalf("expected TryStart to succeed from StateNew")
	}
	if lc.State() != client.StateStarting {
		t.Fatalf("expected StateStarting, got %v", lc.State())
	}

	// Double start should fail
	if lc.TryStart() {
		t.Fatalf("expected second TryStart to fail")
	}

	lc.SetState(client.StateRunning)
	if lc.State() != client.StateRunning {
		t.Fatalf("expected StateRunning, got %v", lc.State())
	}

	if !lc.TryStop() {
		t.Fatalf("expected TryStop to succeed from StateRunning")
	}
	if lc.State() != client.StateStopping {
		t.Fatalf("expected StateStopping, got %v", lc.State())
	}

	lc.SetState(client.StateStopped)
	if lc.State() != client.StateStopped {
		t.Fatalf("expected StateStopped, got %v", lc.State())
	}

	// Can start again after stopped
	if !lc.TryStart() {
		t.Fatalf("expected TryStart to succeed from StateStopped")
	}
}

func TestUserRateLimiter(t *testing.T) {
	limiter := client.NewUserRateLimiter(3, 50*time.Millisecond)

	// User 1 consumes 3 tokens
	if !limiter.Allow(1, "command") {
		t.Fatalf("expected token 1 to be allowed")
	}
	if !limiter.Allow(1, "command") {
		t.Fatalf("expected token 2 to be allowed")
	}
	if !limiter.Allow(1, "command") {
		t.Fatalf("expected token 3 to be allowed")
	}

	// 4th token should be blocked
	if limiter.Allow(1, "command") {
		t.Fatalf("expected 4th token to be blocked")
	}

	// User 2 on another category should have fresh bucket
	if !limiter.Allow(2, "command") {
		t.Fatalf("expected user 2 to be allowed")
	}

	// After refill interval, token should be available
	time.Sleep(60 * time.Millisecond)
	if !limiter.Allow(1, "command") {
		t.Fatalf("expected refilled token to be allowed")
	}
}

func TestUserRateLimiter_Concurrent(t *testing.T) {
	limiter := client.NewUserRateLimiter(10, time.Second)

	var wg sync.WaitGroup
	var allowedCount int32
	var mu sync.Mutex

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Allow(999, "callback") {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowedCount != 10 {
		t.Fatalf("expected exactly 10 requests allowed, got %d", allowedCount)
	}
}

func TestUpdateHandlers_ShutdownBarrier(t *testing.T) {
	dispatcher := tg.NewUpdateDispatcher()
	var messageProcessed bool

	// Handler configured with IsShuttingDown returning true
	deps := client.UpdateHandlerDeps{
		IsShuttingDown: func() bool { return true },
	}
	client.RegisterUpdateHandlers(&dispatcher, deps)

	// Dispatch an update
	update := &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      123,
			Message: "/start",
			PeerID:  &tg.PeerUser{UserID: 42},
		},
	}

	err := dispatcher.Handle(context.Background(), &tg.Updates{
		Updates: []tg.UpdateClass{update},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if messageProcessed {
		t.Fatalf("expected message to be rejected by shutdown barrier")
	}
}
