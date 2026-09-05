package core

import (
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

// Unpin unpins the current message or the replied-to message.
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

// Purge safely purges messages from the replied message up to the current command message,
// taking into account forum topic scope so messages in other topics are never affected.
func (m *MessagesFacade) Purge() (int, error) {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return 0, errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return 0, errors.New("peer is nil")
	}
	if c.Message == nil || c.Message.ReplyToID == 0 {
		return 0, errors.New("purge must be a reply to a message")
	}

	topicID := c.TopicID()
	if topicID == 0 {
		reply, _ := c.GetReply()
		if reply != nil && reply.TopicID != 0 {
			topicID = reply.TopicID
		}
	}

	fromID := c.Message.ReplyToID
	toID := c.Message.ID
	return c.Svc.PurgeMessages(c.Ctx, c.PeerID, topicID, fromID, toID)
}
