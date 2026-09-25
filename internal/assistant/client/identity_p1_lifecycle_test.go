package client

import (
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

func TestP1AssistantRestartNeverExposesStaleInlineUsername(t *testing.T) {
	c := NewAssistantClient(1234, "hash", "token", zap.NewNop())
	old := &tg.User{ID: 100, Username: "old_assistant_bot"}
	old.SetBotInlinePlaceholder("Search")
	c.mu.Lock()
	c.self = old
	c.mu.Unlock()
	c.lifecycle.SetState(StateRunning)

	if got, err := c.InlineUsername(); err != nil || got != "old_assistant_bot" {
		t.Fatalf("InlineUsername(initial)=%q err=%v", got, err)
	}

	// A restart transitions away from Running before the new authenticated bot
	// identity is installed. Keep the old pointer intentionally to prove the
	// readiness check, not pointer clearing, prevents stale identity reuse.
	c.lifecycle.SetState(StateStarting)
	if got, err := c.InlineUsername(); !errors.Is(err, ErrNotReady) || got != "" {
		t.Fatalf("InlineUsername(restarting)=%q err=%v, want empty/%v", got, err, ErrNotReady)
	}

	fresh := &tg.User{ID: 101, Username: "new_assistant_bot"}
	fresh.SetBotInlinePlaceholder("Search")
	c.mu.Lock()
	c.self = fresh
	c.mu.Unlock()
	if got, err := c.InlineUsername(); !errors.Is(err, ErrNotReady) || got != "" {
		t.Fatalf("InlineUsername(new identity before ready)=%q err=%v", got, err)
	}

	c.lifecycle.SetState(StateRunning)
	if got, err := c.InlineUsername(); err != nil || got != "new_assistant_bot" {
		t.Fatalf("InlineUsername(after restart)=%q err=%v", got, err)
	}

	c.lifecycle.SetState(StateFailed)
	if got, err := c.InlineUsername(); !errors.Is(err, ErrNotReady) || got != "" {
		t.Fatalf("InlineUsername(failed transport)=%q err=%v", got, err)
	}
}
