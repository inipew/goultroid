package execution

import (
	"context"
	"fmt"
)

// ChatKind identifies the Telegram conversation class relevant to execution policy.
type ChatKind uint8

const (
	ChatUnknown ChatKind = iota
	ChatPrivate
	ChatGroup
	ChatSupergroup
	ChatChannel
)

// ExecutionContext wraps execution parameters across userbot, assistant, and inline surfaces.
type ExecutionContext struct {
	Context      context.Context
	Source       Source
	Actor        Actor
	ChatID       int64
	ChatKind     ChatKind
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

// SetChatKind records the conversation class used by group/private policy checks.
func (c *ExecutionContext) SetChatKind(kind ChatKind) {
	if c != nil {
		c.ChatKind = kind
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
	return c.Reply(text)
}

// SendToast sends a callback alert or transient notification where supported.
func (c *ExecutionContext) SendToast(text string, alert bool) error {
	if c.toastFunc != nil {
		return c.toastFunc(text, alert)
	}
	return nil
}
