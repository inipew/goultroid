package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
)

// MessagesFacade provides a dedicated namespace for message sending, editing,
// deletion, reaction, and pinning operations.
type MessagesFacade struct {
	ctx *Context
}

// Reply sends a response message to the same chat and records LastResponseID.
func (m *MessagesFacade) Reply(text string) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	sent, err := c.Svc.SendMessage(c.Ctx, c.PeerID, text)
	if err != nil {
		return fmt.Errorf("reply failed: %w", err)
	}
	if sent != nil {
		c.LastResponseID = sent.ID
	}
	return nil
}

// ReplyAndDelete sends a new response and then best-effort deletes the
// incoming command message. Sending is authoritative: if the response fails,
// the trigger is intentionally left intact so the user does not lose the
// command without receiving its result.
func (m *MessagesFacade) ReplyAndDelete(text string) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}

	sent, err := c.Svc.SendMessage(c.Ctx, c.PeerID, text)
	if err != nil {
		return fmt.Errorf("reply failed: %w", err)
	}
	if sent != nil {
		c.LastResponseID = sent.ID
	}

	// Trigger cleanup is deliberately best-effort. The response has already
	// succeeded, so a permission/race/error deleting the user's command must
	// not turn a successful command into an application error.
	if c.Message != nil && c.Message.ID > 0 {
		_ = c.Svc.DeleteMessage(c.Ctx, c.PeerID, []int{c.Message.ID})
	}
	return nil
}

// Edit edits the previously sent response (if Reply was called) or the outgoing command message.
func (m *MessagesFacade) Edit(text string) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	msgID := c.LastResponseID
	if msgID == 0 && c.Message != nil {
		msgID = c.Message.ID
	}
	if msgID == 0 {
		return errors.New("no message to edit")
	}
	return c.Svc.EditMessage(c.Ctx, c.PeerID, msgID, text)
}

// EditOrReply updates an existing bot-owned response or an outgoing userbot command.
func (m *MessagesFacade) EditOrReply(text string) error {
	c := m.ctx
	if c == nil {
		return errors.New("context is nil")
	}
	if c.LastResponseID != 0 {
		return m.Edit(text)
	}
	if c.Message != nil && c.Message.IsOutgoing {
		return m.Edit(text)
	}
	return m.ReplyAndDelete(text)
}

// ReplyMarkup sends a response message to the same chat with reply markup attached.
func (m *MessagesFacade) ReplyMarkup(text string, markup tg.ReplyMarkupClass) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	sent, err := c.Svc.SendMessageWithMarkup(c.Ctx, c.PeerID, text, markup)
	if err != nil {
		return fmt.Errorf("reply markup failed: %w", err)
	}
	if sent != nil {
		c.LastResponseID = sent.ID
	}
	return nil
}

// EditMarkup edits the previously sent response or command message with new text and markup.
func (m *MessagesFacade) EditMarkup(text string, markup tg.ReplyMarkupClass) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	msgID := c.LastResponseID
	if msgID == 0 && c.Message != nil {
		msgID = c.Message.ID
	}
	if msgID == 0 {
		return errors.New("no message to edit")
	}
	return c.Svc.EditMessageMarkup(c.Ctx, c.PeerID, msgID, text, markup)
}

// Delete deletes the current command message.
func (m *MessagesFacade) Delete() error {
	c := m.ctx
	if c == nil || c.Svc == nil {
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

// DeleteResponse deletes the bot's previously sent response message, if any.
func (m *MessagesFacade) DeleteResponse() error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	if c.LastResponseID == 0 {
		return errors.New("no response message to delete")
	}
	return c.Svc.DeleteMessage(c.Ctx, c.PeerID, []int{c.LastResponseID})
}

// React sends an emoji reaction to the message.
func (m *MessagesFacade) React(emoji string) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
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

// Pin pins the current message or the replied-to message.
func (m *MessagesFacade) Pin(silent bool) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	targetID := c.targetMsgID()
	if targetID == 0 {
		return errors.New("no message to pin")
	}
	return c.Svc.PinMessage(c.Ctx, c.PeerID, targetID, silent)
}

// Unpin unpins a message in the chat.
func (m *MessagesFacade) Unpin() error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	targetID := c.targetMsgID()
	if targetID == 0 {
		return errors.New("no message to unpin")
	}
	return c.Svc.UnpinMessage(c.Ctx, c.PeerID, targetID)
}

// Forward forwards the message (or replied message) to another peer.
func (m *MessagesFacade) Forward(toPeer tg.InputPeerClass) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	if toPeer == nil {
		return errors.New("destination peer is nil")
	}
	targetID := c.targetMsgID()
	if targetID == 0 {
		return errors.New("no message to forward")
	}
	return c.Svc.ForwardMessages(c.Ctx, c.PeerID, toPeer, []int{targetID})
}

// ForwardToSelf forwards the message (or replied message) to Saved Messages.
func (m *MessagesFacade) ForwardToSelf() error {
	return m.Forward(&tg.InputPeerSelf{})
}

// Purge safely purges messages from the replied message up to, but not including,
// the current command message. The command is left available so a successful
// result can be sent without editing a message that purge already deleted.
func (m *MessagesFacade) Purge() (int, error) {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return 0, errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return 0, errors.New("peer is nil")
	}
	if c.Message == nil || c.Message.ID <= 0 || c.Message.ReplyToID <= 0 {
		return 0, errors.New("purge must be a reply to a valid message")
	}
	if c.Message.ID <= c.Message.ReplyToID {
		return 0, errors.New("purge command must be newer than the replied message")
	}

	reply, err := c.GetReply()
	if err != nil {
		return 0, err
	}
	if reply == nil {
		return 0, fmt.Errorf("%w: replied message no longer exists", ErrNotFound)
	}

	commandTopic := c.TopicID()
	replyTopic := reply.TopicID
	if commandTopic > 0 && replyTopic == 0 && reply.ID == commandTopic {
		replyTopic = commandTopic
	}
	if commandTopic != 0 || replyTopic != 0 {
		if commandTopic == 0 || replyTopic == 0 || commandTopic != replyTopic {
			return 0, fmt.Errorf("%w: purge range crosses forum topics", ErrInvalidArgs)
		}
	}

	purger, ok := c.Svc.(interface {
		PurgeMessagesSafe(context.Context, tg.InputPeerClass, int, int, int) (int, error)
	})
	if !ok {
		return 0, errors.New("telegram service does not support safe purge")
	}
	return purger.PurgeMessagesSafe(c.Ctx, c.PeerID, commandTopic, c.Message.ReplyToID, c.Message.ID)
}
