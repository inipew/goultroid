package execution

import (
	"context"
	"fmt"
)

// ExecutionContext wraps execution parameters across userbot, assistant, and inline surfaces.
type ExecutionContext struct {
	Context      context.Context
	Source       Source
	Actor        Actor
	ChatID       int64
	MessageID    int
	RawText      string
	Args         []string
	Capabilities CapabilitySet

	replyFunc func(text string) error
	editFunc  func(text string) error
	toastFunc func(text string, alert bool) error
}

// NewExecutionContext constructs a unified ExecutionContext.
func NewExecutionContext(
	ctx context.Context,
	source Source,
	actor Actor,
	chatID int64,
	messageID int,
	rawText string,
	args []string,
) *ExecutionContext {
	if ctx == nil {
		ctx = context.Background()
	}
	return &ExecutionContext{
		Context:      ctx,
		Source:       source,
		Actor:        actor,
		ChatID:       chatID,
		MessageID:    messageID,
		RawText:      rawText,
		Args:         args,
		Capabilities: make(CapabilitySet),
	}
}

// SetHandlers attaches transport response closures.
func (c *ExecutionContext) SetHandlers(
	reply func(string) error,
	edit func(string) error,
	toast func(string, bool) error,
) {
	c.replyFunc = reply
	c.editFunc = edit
	c.toastFunc = toast
}

// Reply sends a response appropriate for the current transport surface.
func (c *ExecutionContext) Reply(text string) error {
	if c.replyFunc != nil {
		return c.replyFunc(text)
	}
	return fmt.Errorf("execution: reply handler not configured for %s", c.Source)
}

// Edit edits the source message in place where supported.
func (c *ExecutionContext) Edit(text string) error {
	if c.editFunc != nil {
		return c.editFunc(text)
	}
	// Fall back to Reply if edit is not directly supported
	return c.Reply(text)
}

// SendToast sends a callback alert or transient notification where supported.
func (c *ExecutionContext) SendToast(text string, alert bool) error {
	if c.toastFunc != nil {
		return c.toastFunc(text, alert)
	}
	return nil
}
