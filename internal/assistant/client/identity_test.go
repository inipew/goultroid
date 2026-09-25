package client

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

func TestAssistantClientUsernameRequiresCurrentRunningIdentity(t *testing.T) {
	c := NewAssistantClient(1234, "hash", "token", zap.NewNop())
	c.mu.Lock()
	c.self = &tg.User{ID: 99, Username: "assistant_bot"}
	c.mu.Unlock()

	for _, state := range []ClientState{StateNew, StateStarting, StateStopping, StateStopped, StateFailed} {
		c.lifecycle.SetState(state)
		if got := c.Username(); got != "" {
			t.Fatalf("Username() in state %s=%q, want empty", state, got)
		}
	}

	c.lifecycle.SetState(StateRunning)
	if got := c.Username(); got != "assistant_bot" {
		t.Fatalf("Username() while running=%q, want assistant_bot", got)
	}

	c.mu.Lock()
	c.self = &tg.User{ID: 99}
	c.mu.Unlock()
	if got := c.Username(); got != "" {
		t.Fatalf("Username() without authenticated username=%q, want empty", got)
	}
}

func TestAssistantClientWaitReadyRejectsStaleClosedReadySignal(t *testing.T) {
	c := NewAssistantClient(1234, "hash", "token", zap.NewNop())
	ready := make(chan struct{})
	close(ready)
	done := make(chan struct{})
	close(done)
	c.mu.Lock()
	c.ready = ready
	c.runDone = done
	c.mu.Unlock()
	c.lifecycle.SetState(StateStopped)

	if err := c.WaitReady(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("WaitReady(stopped) error=%v, want %v", err, ErrNotReady)
	}
}

func TestAssistantClientWaitReadyReturnsCurrentFailure(t *testing.T) {
	c := NewAssistantClient(1234, "hash", "token", zap.NewNop())
	want := errors.New("assistant transport failed")
	ready := make(chan struct{})
	close(ready)
	c.mu.Lock()
	c.ready = ready
	c.lastError = want
	c.mu.Unlock()
	c.lifecycle.SetState(StateFailed)

	if err := c.WaitReady(context.Background()); !errors.Is(err, want) {
		t.Fatalf("WaitReady(failed) error=%v, want %v", err, want)
	}
}

func TestAssistantClientWaitReadyRunningRequiresCurrentIdentity(t *testing.T) {
	c := NewAssistantClient(1234, "hash", "token", zap.NewNop())
	c.lifecycle.SetState(StateRunning)
	if err := c.WaitReady(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("WaitReady(running without identity) error=%v, want %v", err, ErrNotReady)
	}
	c.mu.Lock()
	c.self = &tg.User{ID: 99, Username: "assistant_bot"}
	c.mu.Unlock()
	if err := c.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady(running with identity) error=%v", err)
	}
}

func TestAssistantInlineCapabilityUsesBotInlinePlaceholderPresence(t *testing.T) {
	user := &tg.User{Username: "assistant_bot"}
	if err := inlineCapability(user); !errors.Is(err, ErrInlineDisabled) {
		t.Fatalf("inlineCapability(without placeholder) error=%v, want %v", err, ErrInlineDisabled)
	}

	// gotd peers.Bot.SupportsInline checks presence of this optional field, not
	// whether the placeholder text itself is non-empty.
	user.SetBotInlinePlaceholder("")
	if err := inlineCapability(user); err != nil {
		t.Fatalf("inlineCapability(with placeholder flag) error=%v", err)
	}
}

func TestAssistantClientInlineUsernameFailsClosedUntilInlineModeEnabled(t *testing.T) {
	c := NewAssistantClient(1234, "hash", "token", zap.NewNop())
	user := &tg.User{Username: "assistant_bot"}
	c.mu.Lock()
	c.self = user
	c.mu.Unlock()
	c.lifecycle.SetState(StateRunning)

	if _, err := c.InlineUsername(); !errors.Is(err, ErrInlineDisabled) {
		t.Fatalf("InlineUsername(disabled) error=%v, want %v", err, ErrInlineDisabled)
	}

	user.SetBotInlinePlaceholder("Search...")
	got, err := c.InlineUsername()
	if err != nil || got != "assistant_bot" {
		t.Fatalf("InlineUsername(enabled)=%q err=%v", got, err)
	}

	c.shuttingDown.Store(true)
	if _, err := c.InlineUsername(); !errors.Is(err, ErrNotReady) {
		t.Fatalf("InlineUsername(quiescing) error=%v, want %v", err, ErrNotReady)
	}
}
