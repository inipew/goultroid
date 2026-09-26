package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
)

const delayedDeleteRetainedBytes int64 = 256

// MessagesFacade provides a dedicated namespace for message sending, editing,
// deletion, reaction, and pinning operations.
type MessagesFacade struct {
	ctx *Context
}

func messageSendContext(c *Context) MessageSendContext {
	if c == nil || c.Message == nil {
		return MessageSendContext{}
	}
	replyToID := c.Message.ID
	if replyToID <= 0 && c.Message.TopicID > 0 {
		replyToID = c.Message.TopicID
	}
	return MessageSendContext{ReplyToID: replyToID, TopicID: c.Message.TopicID}
}

func sendContextualMessage(c *Context, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	send := messageSendContext(c)
	if contextual, ok := c.Svc.(ContextualTelegramServicer); ok {
		return contextual.SendMessageContext(c.Ctx, c.PeerID, text, markup, send)
	}
	if send.TopicID > 0 {
		return nil, fmt.Errorf("%w: telegram transport cannot preserve forum topic %d", ErrUnavailable, send.TopicID)
	}
	if markup != nil {
		return c.Svc.SendMessageWithMarkup(c.Ctx, c.PeerID, text, markup)
	}
	return c.Svc.SendMessage(c.Ctx, c.PeerID, text)
}

func (m *MessagesFacade) scheduleDelete(peer tg.InputPeerClass, msgID int, delay time.Duration) error {
	c := m.ctx
	if delay <= 0 || msgID <= 0 {
		return nil
	}
	if c == nil || c.Svc == nil || peer == nil {
		return errors.New("telegram service not initialized")
	}
	if c.DelayedActions == nil {
		return fmt.Errorf("%w: delayed action scheduler is unavailable", ErrUnavailable)
	}
	svc := c.Svc
	return c.DelayedActions.Schedule(c.Ctx, delay, delayedDeleteRetainedBytes, func(actionCtx context.Context) error {
		return svc.DeleteMessage(actionCtx, peer, []int{msgID})
	})
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
	sent, err := sendContextualMessage(c, text, nil)
	if err != nil {
		return fmt.Errorf("reply failed: %w", err)
	}
	if sent != nil {
		c.LastResponseID = sent.ID
	}
	return nil
}

func (m *MessagesFacade) ReplyAndDelete(text string) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	sent, err := sendContextualMessage(c, text, nil)
	if err != nil {
		return fmt.Errorf("reply failed: %w", err)
	}
	if sent != nil {
		c.LastResponseID = sent.ID
	}
	if c.Message != nil && c.Message.ID > 0 {
		_ = c.Svc.DeleteMessage(c.Ctx, c.PeerID, []int{c.Message.ID})
	}
	return nil
}

func (m *MessagesFacade) ReplyAndDeleteWithDelay(text string, delay time.Duration) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	sent, err := sendContextualMessage(c, text, nil)
	if err != nil {
		return fmt.Errorf("reply failed: %w", err)
	}
	if sent != nil {
		c.LastResponseID = sent.ID
	}
	if c.Message != nil && c.Message.ID > 0 {
		_ = c.Svc.DeleteMessage(c.Ctx, c.PeerID, []int{c.Message.ID})
	}
	if delay > 0 && sent != nil && sent.ID > 0 {
		if err := m.scheduleDelete(c.PeerID, sent.ID, delay); err != nil {
			return err
		}
	}
	return nil
}

// Edit edits the previously sent response or the current trigger message.
// Direct Edit keeps its legacy target selection, but every transport attempt is
// stage-aware so callers can distinguish preflight failure from an ambiguous
// Telegram edit delivery.
func (m *MessagesFacade) Edit(text string) error {
	c := m.ctx
	if c == nil {
		return messageEditFailure(MessageEditStagePreflight, false, errors.New("context is nil"))
	}
	msgID := c.LastResponseID
	outgoingTrigger := false
	if msgID == 0 && c.Message != nil {
		msgID = c.Message.ID
		outgoingTrigger = c.Message.IsOutgoing && msgID > 0
	}
	if err := m.editMessageID(text, msgID); err != nil {
		return err
	}
	if outgoingTrigger && c.LastResponseID == 0 {
		c.LastResponseID = msgID
	}
	return nil
}

// EditOrReply chooses its mutation mode before issuing any Telegram RPC.
// A known bot-owned response or outgoing userbot trigger is edited. If no
// editable anchor exists, it replies immediately. Once an edit RPC is attempted,
// any error is returned without reply fallback because Telegram may have already
// committed the edit.
func (m *MessagesFacade) EditOrReply(text string) error {
	c := m.ctx
	if c == nil {
		return errors.New("context is nil")
	}
	if msgID, outgoingTrigger, ok := editableResponseAnchor(c); ok {
		if err := m.editMessageID(text, msgID); err != nil {
			return err
		}
		if outgoingTrigger && c.LastResponseID == 0 {
			c.LastResponseID = msgID
		}
		return nil
	}
	if c.Source != ExecutionInteractive {
		return m.Reply(text)
	}
	return m.ReplyAndDelete(text)
}

// EditOrReplyWithDelay follows the same preflight routing as EditOrReply. A
// failed edit never falls back to a reply, and a post-edit cleanup failure is
// marked as already committed so callers cannot safely emit another response.
func (m *MessagesFacade) EditOrReplyWithDelay(text string, delay time.Duration) error {
	c := m.ctx
	if c == nil {
		return errors.New("context is nil")
	}
	if msgID, outgoingTrigger, ok := editableResponseAnchor(c); ok {
		if err := m.editMessageID(text, msgID); err != nil {
			return err
		}
		if outgoingTrigger && c.LastResponseID == 0 {
			c.LastResponseID = msgID
		}
		if delay > 0 {
			if err := m.scheduleDelete(c.PeerID, msgID, delay); err != nil {
				return messageEditFailure(MessageEditStagePostCommit, true, err)
			}
		}
		return nil
	}
	if c.Source != ExecutionInteractive {
		if err := m.Reply(text); err != nil {
			return err
		}
		if delay > 0 && c.LastResponseID > 0 && c.Svc != nil && c.PeerID != nil {
			return m.scheduleDelete(c.PeerID, c.LastResponseID, delay)
		}
		return nil
	}
	return m.ReplyAndDeleteWithDelay(text, delay)
}

func (m *MessagesFacade) ReplyMarkup(text string, markup tg.ReplyMarkupClass) error {
	c := m.ctx
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	sent, err := sendContextualMessage(c, text, markup)
	if err != nil {
		return fmt.Errorf("reply markup failed: %w", err)
	}
	if sent != nil {
		c.LastResponseID = sent.ID
	}
	return nil
}

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

func (m *MessagesFacade) ForwardToSelf() error {
	return m.Forward(&tg.InputPeerSelf{})
}

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
