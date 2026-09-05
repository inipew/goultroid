package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
)

// TelegramServicer defines message and reaction actions on Telegram.
// Mockable for unit testing without a live MTProto connection.
type TelegramServicer interface {
	SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error)
	EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error
	DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error
	React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error
	GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error)
}

// Message represents a high-level Telegram message.
type Message struct {
	ID        int
	Text      string
	Date      time.Time
	ReplyToID int
	MediaType string
}

// Chat represents the chat in which an event occurred.
type Chat struct {
	ID       int64
	Title    string
	Username string
	Type     string // "private", "group", "supergroup", "channel"
}

// User represents the user who sent the message.
type User struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	IsBot     bool
}

// Context is passed to each command handler, providing clean abstractions.
type Context struct {
	Ctx context.Context

	Command string
	Args    []string
	RawArgs string

	Message *Message
	Chat    *Chat
	Sender  *User
	Perms   *Permissions

	Svc    TelegramServicer
	PeerID tg.InputPeerClass
}

// Reply sends a response message to the same chat.
func (c *Context) Reply(text string) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	sent, err := c.Svc.SendMessage(c.Ctx, c.PeerID, text)
	if err != nil {
		return fmt.Errorf("reply failed: %w", err)
	}
	// If message was successfully sent, we can track sent message ID for future edits
	if sent != nil && c.Message != nil {
		// update context message ID if this was an edit target or userbot self-reply
		c.Message.ID = sent.ID
	}
	return nil
}

// Edit edits the command message (if sent by self) or a previously sent response.
func (c *Context) Edit(text string) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	if c.Message == nil || c.Message.ID == 0 {
		return errors.New("no message to edit")
	}
	return c.Svc.EditMessage(c.Ctx, c.PeerID, c.Message.ID, text)
}

// Delete deletes the current message.
func (c *Context) Delete() error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	if c.Message == nil || c.Message.ID == 0 {
		return errors.New("no message to delete")
	}
	return c.Svc.DeleteMessage(c.Ctx, c.PeerID, []int{c.Message.ID})
}

// React sends an emoji reaction to the message.
func (c *Context) React(emoji string) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	if c.Message == nil || c.Message.ID == 0 {
		return errors.New("no message to react to")
	}
	return c.Svc.React(c.Ctx, c.PeerID, c.Message.ID, emoji)
}

// GetReply retrieves the message that was replied to, if any.
func (c *Context) GetReply() (*Message, error) {
	if c.Svc == nil {
		return nil, errors.New("telegram service not initialized")
	}
	if c.Message == nil || c.Message.ReplyToID == 0 {
		return nil, nil
	}
	msg, err := c.Svc.GetMessage(c.Ctx, c.PeerID, c.Message.ReplyToID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch reply message: %w", err)
	}
	if msg == nil {
		return nil, nil
	}
	return &Message{
		ID:   msg.ID,
		Text: msg.Message,
		Date: time.Unix(int64(msg.Date), 0),
	}, nil
}

// IsPrivate returns true if the chat is a 1-on-1 private chat.
func (c *Context) IsPrivate() bool {
	return c.Chat != nil && c.Chat.Type == "private"
}

// IsGroup returns true if the chat is a group or supergroup.
func (c *Context) IsGroup() bool {
	return c.Chat != nil && (c.Chat.Type == "group" || c.Chat.Type == "supergroup")
}

// IsChannel returns true if the chat is a broadcast channel.
func (c *Context) IsChannel() bool {
	return c.Chat != nil && c.Chat.Type == "channel"
}

// IsOwner returns true if the sender is the Owner.
func (c *Context) IsOwner() bool {
	if c.Sender == nil || c.Perms == nil {
		return false
	}
	return c.Perms.IsOwner(c.Sender.ID)
}

// IsSudo returns true if the sender has Sudo or Owner privileges.
func (c *Context) IsSudo() bool {
	if c.Sender == nil || c.Perms == nil {
		return false
	}
	return c.Perms.IsSudo(c.Sender.ID)
}
