package client

import (
	"context"
	"errors"
	"strings"

	"github.com/gotd/td/tg"
)

var (
	ErrNotReady       = errors.New("assistant/client: client is not ready")
	ErrInlineDisabled = errors.New("assistant inline mode is disabled; enable it with @BotFather /setinline, then restart Goultroid")
)

// inlineCapability mirrors gotd peers.Bot.SupportsInline: Telegram exposes
// inline-mode support through presence of bot_inline_placeholder on the
// authenticated bot user. No extra RPC is needed for this preflight.
func inlineCapability(user *tg.User) error {
	if user == nil || strings.TrimSpace(user.Username) == "" {
		return ErrNotReady
	}
	if _, ok := user.GetBotInlinePlaceholder(); !ok {
		return ErrInlineDisabled
	}
	return nil
}

// InlineUsername returns the current Assistant username only when the current
// authenticated bot identity is ready and Telegram reports inline mode enabled.
// Self-inline presentation therefore fails before getInlineBotResults when
// @BotFather /setinline has not been configured.
func (c *AssistantClient) InlineUsername() (string, error) {
	if c == nil || c.lifecycle == nil || c.lifecycle.State() != StateRunning || c.shuttingDown.Load() {
		return "", ErrNotReady
	}
	c.mu.RLock()
	user := c.self
	c.mu.RUnlock()
	if c.lifecycle.State() != StateRunning || c.shuttingDown.Load() {
		return "", ErrNotReady
	}
	if err := inlineCapability(user); err != nil {
		return "", err
	}
	return strings.TrimSpace(user.Username), nil
}

// WaitReady waits for the current Assistant run to become ready. A readiness
// signal from an older run is never accepted after stop/failure/restart.
func (c *AssistantClient) WaitReady(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.lifecycle == nil {
		return ErrNotReady
	}

	switch c.lifecycle.State() {
	case StateRunning:
		if c.Username() == "" {
			return ErrNotReady
		}
		return nil
	case StateStarting:
		c.mu.RLock()
		ready := c.ready
		done := c.runDone
		c.mu.RUnlock()
		if ready == nil || done == nil {
			return ErrNotReady
		}
		select {
		case <-ready:
			if c.lifecycle.State() == StateRunning && c.Username() != "" {
				return nil
			}
			if err := c.LastError(); err != nil {
				return err
			}
			return ErrNotReady
		case <-done:
			if err := c.LastError(); err != nil {
				return err
			}
			return ErrNotReady
		case <-ctx.Done():
			return ctx.Err()
		}
	case StateFailed:
		if err := c.LastError(); err != nil {
			return err
		}
		return ErrNotReady
	default:
		return ErrNotReady
	}
}

// Username returns the authenticated username for the currently running
// Assistant transport. It returns an empty string before readiness and after
// stopping/failure so consumers fail closed instead of using stale identity.
func (c *AssistantClient) Username() string {
	if c == nil || c.lifecycle == nil || c.lifecycle.State() != StateRunning {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.self == nil {
		return ""
	}
	return c.self.Username
}
