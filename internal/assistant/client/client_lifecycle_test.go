package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestAssistantClientStopTimeoutCanBeRetried(t *testing.T) {
	done := make(chan struct{})
	c := &AssistantClient{
		logger:    zap.NewNop(),
		lifecycle: NewLifecycle(),
		runDone:   done,
		cancel:    func() {},
	}
	c.lifecycle.SetState(StateRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := c.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want context deadline exceeded", err)
	}
	if got := c.lifecycle.State(); got != StateStopping {
		t.Fatalf("state after timed-out Stop() = %v, want stopping", got)
	}

	result := make(chan error, 1)
	go func() { result <- c.Stop(context.Background()) }()
	select {
	case err := <-result:
		t.Fatalf("retry returned before client stopped: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	c.lifecycle.SetState(StateStopped)
	close(done)
	if err := <-result; err != nil {
		t.Fatalf("retried Stop() error = %v", err)
	}
}

func TestWaitForStartupWaitsForReadiness(t *testing.T) {
	ready := make(chan struct{})
	errCh := make(chan error, 1)
	result := make(chan error, 1)
	go func() {
		result <- waitForStartup(context.Background(), ready, errCh)
	}()

	select {
	case err := <-result:
		t.Fatalf("startup returned before readiness: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(ready)
	if err := <-result; err != nil {
		t.Fatalf("startup readiness error = %v", err)
	}
}

func TestWaitForStartupPropagatesFailure(t *testing.T) {
	ready := make(chan struct{})
	errCh := make(chan error, 1)
	want := errors.New("authentication failed")
	errCh <- want
	if err := waitForStartup(context.Background(), ready, errCh); !errors.Is(err, want) {
		t.Fatalf("startup error = %v, want %v", err, want)
	}
}

func TestAssistantClientStartDoesNotWaitForTelegramReadiness(t *testing.T) {
	c := NewAssistantClient(1234, "hash", "test-token", zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())

	startedAt := time.Now()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("Start() blocked for Telegram readiness: %v", elapsed)
	}
	cancel()
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestAssistantClientStartRejectsCanceledContext(t *testing.T) {
	c := NewAssistantClient(1234, "hash", "test-token", zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context canceled", err)
	}
	if got := c.State(); got != StateNew {
		t.Fatalf("state after canceled Start() = %v, want new", got)
	}
}
