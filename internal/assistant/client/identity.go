package client

import (
	"context"
	"errors"
)

var ErrNotReady = errors.New("assistant/client: client is not ready")

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
