package core

import (
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/presentation"
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
		if err := c.Messages().scheduleDelete(c.PeerID, c.LastResponseID, opts.AutoDeleteDelay); err != nil {
			return err
		}
	}
	return nil
}

// SemanticResponse routes a transport-neutral response intent through the
// canonical edit-or-reply behavior. Userbot and Assistant commands therefore
// share the same progress replacement, reply/thread preservation, and final
// result target without adding a second presentation runtime.
func (c *Context) SemanticResponse(response presentation.Response) error {
	return c.Messages().SemanticResponse(response)
}

func (c *Context) Status(text string) error   { return c.SemanticResponse(presentation.Status(text)) }
func (c *Context) Success(text string) error  { return c.SemanticResponse(presentation.Success(text)) }
func (c *Context) Error(text string) error    { return c.SemanticResponse(presentation.Error(text)) }
func (c *Context) Progress(text string) error { return c.SemanticResponse(presentation.Progress(text)) }
func (c *Context) Result(text string) error   { return c.SemanticResponse(presentation.Result(text)) }

// SemanticResponse is the message-facade form used by code that intentionally
// works through the facade namespace rather than Context convenience methods.
func (m *MessagesFacade) SemanticResponse(response presentation.Response) error {
	text := response.Render()
	if strings.TrimSpace(text) == "" {
		return ErrInvalidArgs
	}
	c := m.ctx
	if c != nil && c.LastResponseID == 0 && c.Message != nil && c.Message.IsOutgoing && c.Message.ID > 0 {
		if err := m.Edit(text); err != nil {
			return err
		}
		// The edited outgoing command is now the semantic response anchor. This
		// lets media-producing commands clean up progress uniformly across
		// userbot and Assistant surfaces via DeleteResponse.
		c.LastResponseID = c.Message.ID
		return nil
	}
	return m.EditOrReply(text)
}

func (m *MessagesFacade) Status(text string) error {
	return m.SemanticResponse(presentation.Status(text))
}
func (m *MessagesFacade) Success(text string) error {
	return m.SemanticResponse(presentation.Success(text))
}
func (m *MessagesFacade) Error(text string) error {
	return m.SemanticResponse(presentation.Error(text))
}
func (m *MessagesFacade) Progress(text string) error {
	return m.SemanticResponse(presentation.Progress(text))
}
func (m *MessagesFacade) Result(text string) error {
	return m.SemanticResponse(presentation.Result(text))
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
