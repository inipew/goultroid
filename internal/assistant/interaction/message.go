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
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// PeerReResolver refreshes a peer access hash upon encountering ACCESS_HASH_INVALID.
type PeerReResolver interface {
	InvalidatePeer(peer tg.InputPeerClass)
	ReResolve(ctx context.Context, inputPeer tg.InputPeerClass) (tg.InputPeerClass, error)
}

// ClientInteraction implements MessageInteraction using MTProto TelegramAPI.
type ClientInteraction struct {
	api        TelegramAPI
	logger     *zap.Logger
	reResolver PeerReResolver
	metrics    core.MetricsCollector
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

// SetMetricsCollector configures optional runtime metrics collection.
func (c *ClientInteraction) SetMetricsCollector(m core.MetricsCollector) {
	c.metrics = m
}

// SetPeerReResolver configures a resolver called when ACCESS_HASH_INVALID is detected.
func (c *ClientInteraction) SetPeerReResolver(reResolver PeerReResolver) {
	c.reResolver = reResolver
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
func (c *ClientInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) (retErr error) {
	if c.api == nil {
		return ErrInvalidTarget
	}
	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.RecordTelegramRequest("MessagesSetBotCallbackAnswer", time.Since(start), retErr)
		}
	}()

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
		retErr = fmt.Errorf("assistant answer callback query: %w", classified)
		return retErr
	}
	return nil
}

// Edit updates the text and inline markup of a dialog message with bounded stale hash recovery.
func (c *ClientInteraction) Edit(ctx context.Context, target MessageTarget, text string, markup tg.ReplyMarkupClass) (retErr error) {
	if c.api == nil || !target.IsValid() {
		return ErrInvalidTarget
	}
	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.RecordTelegramRequest("MessagesEditMessage", time.Since(start), retErr)
		}
	}()

	plain, ents := parseHTML(text)
	req := &tg.MessagesEditMessageRequest{
		Peer: target.Peer(),
		ID:   target.MessageID(),
	}
	req.SetMessage(plain)
	if len(ents) > 0 {
		req.SetEntities(ents)
	}
	req.SetNoWebpage(true)
	if markup != nil {
		req.SetReplyMarkup(markup)
	}

	for attempt := 0; attempt <= MaxPeerRecoveryAttempts; attempt++ {
		_, err := c.api.MessagesEditMessage(ctx, req)
		if err == nil {
			return nil
		}
		classified := ClassifyRPCError(err)
		if classified == nil {
			return nil // MESSAGE_NOT_MODIFIED is a no-op success
		}
		if errors.Is(classified, ErrAccessHashStale) && c.reResolver != nil && attempt < MaxPeerRecoveryAttempts {
			c.logger.Warn("assistant: access hash stale during edit, invalidating and re-resolving",
				zap.Int("msg_id", target.MessageID()),
			)
			c.reResolver.InvalidatePeer(req.Peer)
			if newPeer, rerr := c.reResolver.ReResolve(ctx, req.Peer); rerr == nil && newPeer != nil {
				req.Peer = newPeer
				continue // retry once
			}
		}
		return fmt.Errorf("assistant edit message: %w", classified)
	}
	return ErrAccessHashStale
}

// EditMarkup updates only the reply markup of a dialog message.
func (c *ClientInteraction) EditMarkup(ctx context.Context, target MessageTarget, markup tg.ReplyMarkupClass) error {
	if c.api == nil || !target.IsValid() {
		return ErrInvalidTarget
	}

	req := &tg.MessagesEditMessageRequest{
		Peer: target.Peer(),
		ID:   target.MessageID(),
	}
	if markup != nil {
		req.SetReplyMarkup(markup)
	}

	for attempt := 0; attempt <= MaxPeerRecoveryAttempts; attempt++ {
		_, err := c.api.MessagesEditMessage(ctx, req)
		if err == nil {
			return nil
		}
		classified := ClassifyRPCError(err)
		if classified == nil {
			return nil
		}
		if errors.Is(classified, ErrAccessHashStale) && c.reResolver != nil && attempt < MaxPeerRecoveryAttempts {
			c.reResolver.InvalidatePeer(req.Peer)
			if newPeer, rerr := c.reResolver.ReResolve(ctx, req.Peer); rerr == nil && newPeer != nil {
				req.Peer = newPeer
				continue
			}
		}
		return fmt.Errorf("assistant edit message markup: %w", classified)
	}
	return ErrAccessHashStale
}

// Delete removes a dialog message using peer-aware MTProto RPC with idempotent semantics and stale hash recovery.
func (c *ClientInteraction) Delete(ctx context.Context, target MessageTarget) (retErr error) {
	if c.api == nil || !target.IsValid() {
		return ErrInvalidTarget
	}
	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.RecordTelegramRequest("MessagesDeleteMessages", time.Since(start), retErr)
		}
	}()

	currentPeer := target.Peer()
	for attempt := 0; attempt <= MaxPeerRecoveryAttempts; attempt++ {
		var rpcErr error
		switch p := currentPeer.(type) {
		case *tg.InputPeerChannel:
			_, rpcErr = c.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
				Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
				ID:      []int{target.MessageID()},
			})
		case *tg.InputPeerUser, *tg.InputPeerChat, *tg.InputPeerSelf:
			req := &tg.MessagesDeleteMessagesRequest{ID: []int{target.MessageID()}}
			req.SetRevoke(true)
			_, rpcErr = c.api.MessagesDeleteMessages(ctx, req)
		default:
			retErr = fmt.Errorf("%w: unsupported peer %T", ErrUnsupportedTarget, currentPeer)
			return retErr
		}

		if rpcErr == nil {
			return nil
		}
		classified := ClassifyRPCError(rpcErr)
		if errors.Is(classified, ErrMessageAlreadyDeleted) {
			c.logger.Debug("assistant delete: message already absent, treating as success",
				zap.Int("msg_id", target.MessageID()),
			)
			return nil
		}
		if errors.Is(classified, ErrAccessHashStale) && c.reResolver != nil && attempt < MaxPeerRecoveryAttempts {
			c.logger.Warn("assistant: access hash stale during delete, invalidating and re-resolving",
				zap.Int("msg_id", target.MessageID()),
			)
			c.reResolver.InvalidatePeer(currentPeer)
			if newPeer, rerr := c.reResolver.ReResolve(ctx, currentPeer); rerr == nil && newPeer != nil {
				currentPeer = newPeer
				continue // retry once
			}
		}
		retErr = fmt.Errorf("assistant delete message: %w", classified)
		return retErr
	}
	retErr = ErrAccessHashStale
	return retErr
}

// GetMessage retrieves a message using peer-aware MTProto RPC with bounded stale hash recovery.
func (c *ClientInteraction) GetMessage(ctx context.Context, target MessageTarget) (_ *tg.Message, retErr error) {
	if c.api == nil || !target.IsValid() {
		return nil, ErrInvalidTarget
	}
	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.RecordTelegramRequest("MessagesGetMessages", time.Since(start), retErr)
		}
	}()

	currentPeer := target.Peer()
	for attempt := 0; attempt <= MaxPeerRecoveryAttempts; attempt++ {
		var res tg.MessagesMessagesClass
		var rpcErr error

		switch p := currentPeer.(type) {
		case *tg.InputPeerChannel:
			res, rpcErr = c.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
				Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
				ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: target.MessageID()}},
			})
		default:
			res, rpcErr = c.api.MessagesGetMessages(ctx, []tg.InputMessageClass{
				&tg.InputMessageID{ID: target.MessageID()},
			})
		}

		if rpcErr != nil {
			classified := ClassifyRPCError(rpcErr)
			if errors.Is(classified, ErrAccessHashStale) && c.reResolver != nil && attempt < MaxPeerRecoveryAttempts {
				c.reResolver.InvalidatePeer(currentPeer)
				if newPeer, rerr := c.reResolver.ReResolve(ctx, currentPeer); rerr == nil && newPeer != nil {
					currentPeer = newPeer
					continue
				}
			}
			return nil, classified
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
	return nil, ErrAccessHashStale
}

// SendMessage sends a new message to a peer with optional reply markup.
func (c *ClientInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (_ *tg.Message, retErr error) {
	if c.api == nil || peer == nil {
		return nil, ErrInvalidTarget
	}
	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.RecordTelegramRequest("MessagesSendMessage", time.Since(start), retErr)
		}
	}()

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
		retErr = fmt.Errorf("assistant SendMessage: %w", ClassifyRPCError(err))
		return nil, retErr
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
func (i *InlineClientInteraction) Edit(ctx context.Context, target InlineTarget, text string, markup tg.ReplyMarkupClass) (retErr error) {
	if i.ci == nil || i.ci.api == nil || !target.IsValid() {
		return ErrInvalidTarget
	}
	start := time.Now()
	defer func() {
		if i.ci != nil && i.ci.metrics != nil {
			i.ci.metrics.RecordTelegramRequest("MessagesEditInlineBotMessage", time.Since(start), retErr)
		}
	}()

	plain, ents := parseHTML(text)
	req := &tg.MessagesEditInlineBotMessageRequest{
		ID: target.MessageID(),
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
		retErr = fmt.Errorf("assistant edit inline message: %w", classified)
		return retErr
	}
	return nil
}
