package interaction

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// ClientInteraction implements MessageInteraction using MTProto TelegramAPI.
type ClientInteraction struct {
	api    TelegramAPI
	logger *zap.Logger
}

var _ MessageInteraction = (*ClientInteraction)(nil)

// NewClientInteraction creates an interaction engine backed by a Telegram API instance.
func NewClientInteraction(api TelegramAPI, logger *zap.Logger) *ClientInteraction {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ClientInteraction{
		api:    api,
		logger: logger,
	}
}

func parseHTML(text string) (string, []tg.MessageEntityClass) {
	var eb entity.Builder
	if err := styling.Perform(&eb, html.String(nil, text)); err == nil {
		plain, ents := eb.Complete()
		return plain, ents
	}
	return text, nil
}

func randomID() int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return time.Now().UnixNano()
	}
	return n.Int64()
}

func extractMessage(u tg.UpdatesClass) *tg.Message {
	switch upd := u.(type) {
	case *tg.Updates:
		for _, item := range upd.Updates {
			if newMsg, ok := item.(*tg.UpdateNewMessage); ok {
				if msg, ok := newMsg.Message.(*tg.Message); ok {
					return msg
				}
			}
			if newChannelMsg, ok := item.(*tg.UpdateNewChannelMessage); ok {
				if msg, ok := newChannelMsg.Message.(*tg.Message); ok {
					return msg
				}
			}
		}
	case *tg.UpdateShortSentMessage:
		return &tg.Message{
			ID:   upd.ID,
			Date: upd.Date,
		}
	case *tg.UpdateShortMessage:
		return &tg.Message{
			ID:      upd.ID,
			Message: upd.Message,
			Date:    upd.Date,
		}
	}
	return nil
}

// Answer sends an acknowledgement or popup alert for a callback query.
func (c *ClientInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	if c.api == nil {
		return ErrInvalidTarget
	}
	req := &tg.MessagesSetBotCallbackAnswerRequest{
		QueryID: queryID,
		Alert:   alert,
	}
	if text != "" {
		req.SetMessage(text)
	}
	_, err := c.api.MessagesSetBotCallbackAnswer(ctx, req)
	if err != nil {
		classified := ClassifyRPCError(err)
		if errors.Is(classified, ErrCallbackExpired) {
			c.logger.Debug("callback answer skipped: query already expired", zap.Int64("query_id", queryID))
			return nil
		}
		return fmt.Errorf("assistant answer callback query: %w", classified)
	}
	return nil
}

// Edit updates the text and inline markup of a dialog message.
func (c *ClientInteraction) Edit(ctx context.Context, target MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	if c.api == nil {
		return ErrInvalidTarget
	}
	if !target.IsValid() {
		return ErrInvalidTarget
	}

	plain, ents := parseHTML(text)
	req := &tg.MessagesEditMessageRequest{
		Peer: target.Peer,
		ID:   target.MessageID,
	}
	req.SetMessage(plain)
	if len(ents) > 0 {
		req.SetEntities(ents)
	}
	req.SetNoWebpage(true)
	if markup != nil {
		req.SetReplyMarkup(markup)
	}

	_, err := c.api.MessagesEditMessage(ctx, req)
	if err != nil {
		classified := ClassifyRPCError(err)
		if classified == nil {
			return nil // MESSAGE_NOT_MODIFIED is a no-op success
		}
		return fmt.Errorf("assistant edit message: %w", classified)
	}
	return nil
}

// EditMarkup updates only the reply markup of a dialog message.
func (c *ClientInteraction) EditMarkup(ctx context.Context, target MessageTarget, markup tg.ReplyMarkupClass) error {
	if c.api == nil {
		return ErrInvalidTarget
	}
	if !target.IsValid() {
		return ErrInvalidTarget
	}

	req := &tg.MessagesEditMessageRequest{
		Peer: target.Peer,
		ID:   target.MessageID,
	}
	if markup != nil {
		req.SetReplyMarkup(markup)
	}

	_, err := c.api.MessagesEditMessage(ctx, req)
	if err != nil {
		classified := ClassifyRPCError(err)
		if classified == nil {
			return nil
		}
		return fmt.Errorf("assistant edit message markup: %w", classified)
	}
	return nil
}

// Delete removes a dialog message using peer-aware MTProto RPC with idempotent semantics.
func (c *ClientInteraction) Delete(ctx context.Context, target MessageTarget) error {
	if c.api == nil {
		return ErrInvalidTarget
	}
	if !target.IsValid() {
		return ErrInvalidTarget
	}

	var rpcErr error
	switch p := target.Peer.(type) {
	case *tg.InputPeerChannel:
		_, rpcErr = c.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
			ID:      []int{target.MessageID},
		})
	case *tg.InputPeerUser, *tg.InputPeerChat, *tg.InputPeerSelf:
		req := &tg.MessagesDeleteMessagesRequest{ID: []int{target.MessageID}}
		req.SetRevoke(true)
		_, rpcErr = c.api.MessagesDeleteMessages(ctx, req)
	default:
		return fmt.Errorf("%w: unsupported peer %T", ErrUnsupportedTarget, target.Peer)
	}

	if rpcErr != nil {
		classified := ClassifyRPCError(rpcErr)
		// Idempotency: if message is already absent or was deleted concurrently, consider success
		if errors.Is(classified, ErrMessageAlreadyDeleted) {
			c.logger.Debug("assistant delete: message already absent, treating as success",
				zap.Int("msg_id", target.MessageID),
			)
			return nil
		}
		return fmt.Errorf("assistant delete message: %w", classified)
	}
	return nil
}

// GetMessage retrieves a message using peer-aware MTProto RPC.
func (c *ClientInteraction) GetMessage(ctx context.Context, target MessageTarget) (*tg.Message, error) {
	if c.api == nil {
		return nil, ErrInvalidTarget
	}
	if !target.IsValid() {
		return nil, ErrInvalidTarget
	}

	var res tg.MessagesMessagesClass
	var rpcErr error

	switch p := target.Peer.(type) {
	case *tg.InputPeerChannel:
		res, rpcErr = c.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: target.MessageID}},
		})
	default:
		res, rpcErr = c.api.MessagesGetMessages(ctx, []tg.InputMessageClass{
			&tg.InputMessageID{ID: target.MessageID},
		})
	}

	if rpcErr != nil {
		return nil, ClassifyRPCError(rpcErr)
	}

	extractFirst := func(slice []tg.MessageClass) *tg.Message {
		if len(slice) > 0 {
			if m, ok := slice[0].(*tg.Message); ok {
				return m
			}
		}
		return nil
	}

	switch msgs := res.(type) {
	case *tg.MessagesMessages:
		if m := extractFirst(msgs.Messages); m != nil {
			return m, nil
		}
	case *tg.MessagesMessagesSlice:
		if m := extractFirst(msgs.Messages); m != nil {
			return m, nil
		}
	case *tg.MessagesChannelMessages:
		if m := extractFirst(msgs.Messages); m != nil {
			return m, nil
		}
	}

	return nil, ErrMessageNotFound
}

// SendMessage sends a new message to a peer with optional reply markup.
func (c *ClientInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if c.api == nil || peer == nil {
		return nil, ErrInvalidTarget
	}

	plain, ents := parseHTML(text)
	req := &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  plain,
		RandomID: randomID(),
	}
	if len(ents) > 0 {
		req.SetEntities(ents)
	}
	if markup != nil {
		req.SetReplyMarkup(markup)
	}

	updates, err := c.api.MessagesSendMessage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("assistant SendMessage: %w", ClassifyRPCError(err))
	}
	return extractMessage(updates), nil
}

// InlineClientInteraction adapts ClientInteraction to the InlineInteraction interface.
type InlineClientInteraction struct {
	ci *ClientInteraction
}

var _ InlineInteraction = (*InlineClientInteraction)(nil)

// AsInline returns an InlineInteraction instance backed by this ClientInteraction.
func (c *ClientInteraction) AsInline() *InlineClientInteraction {
	return &InlineClientInteraction{ci: c}
}

// Answer sends callback answer for inline callback query.
func (i *InlineClientInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	return i.ci.Answer(ctx, queryID, text, alert)
}

// Edit updates an inline-sent bot message text and markup.
func (i *InlineClientInteraction) Edit(ctx context.Context, target InlineTarget, text string, markup tg.ReplyMarkupClass) error {
	if i.ci == nil || i.ci.api == nil {
		return ErrInvalidTarget
	}
	if !target.IsValid() {
		return ErrInvalidTarget
	}

	plain, ents := parseHTML(text)
	req := &tg.MessagesEditInlineBotMessageRequest{
		ID: target.MessageID,
	}
	req.SetMessage(plain)
	if len(ents) > 0 {
		req.SetEntities(ents)
	}
	req.SetNoWebpage(true)
	if markup != nil {
		req.SetReplyMarkup(markup)
	}

	_, err := i.ci.api.MessagesEditInlineBotMessage(ctx, req)
	if err != nil {
		classified := ClassifyRPCError(err)
		if classified == nil {
			return nil
		}
		return fmt.Errorf("assistant edit inline message: %w", classified)
	}
	return nil
}
