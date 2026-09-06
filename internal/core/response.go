package core

import (
	"context"
	"time"
)

// ResponseOptions controls the behavior of a normal command response.
type ResponseOptions struct {
	// DeleteTrigger removes the incoming command message after the response
	// has been successfully sent. Deletion is best-effort.
	DeleteTrigger bool
	// AutoDeleteDelay removes the response message itself after the specified duration.
	AutoDeleteDelay time.Duration
}

// Respond sends a response and optionally cleans up the incoming command and schedules response self-destruction.
// The response is sent first so cleanup can never hide a successful response.
func (c *Context) Respond(text string, opts ResponseOptions) error {
	if err := c.Messages().Reply(text); err != nil {
		return err
	}
	if opts.DeleteTrigger {
		_ = c.Messages().Delete()
	}
	if opts.AutoDeleteDelay > 0 && c.LastResponseID > 0 && c.Svc != nil && c.PeerID != nil {
		respID := c.LastResponseID
		peer := c.PeerID
		svc := c.Svc
		time.AfterFunc(opts.AutoDeleteDelay, func() {
			delCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = svc.DeleteMessage(delCtx, peer, []int{respID})
		})
	}
	return nil
}

// ReplyAndDelete is the convenience API for the common userbot command flow.
func (c *Context) ReplyAndDelete(text string) error {
	return c.Respond(text, ResponseOptions{DeleteTrigger: true})
}

// ReplyAndDeleteWithDelay sends a response, deletes the trigger command message,
// and auto-deletes the response after autoDeleteDelay.
func (c *Context) ReplyAndDeleteWithDelay(text string, autoDeleteDelay time.Duration) error {
	return c.Respond(text, ResponseOptions{DeleteTrigger: true, AutoDeleteDelay: autoDeleteDelay})
}
