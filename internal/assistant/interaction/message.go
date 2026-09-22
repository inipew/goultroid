package interaction

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// PeerReResolver refreshes a peer access hash upon encountering ACCESS_HASH_INVALID.
type PeerReResolver interface {
	InvalidatePeer(peer tg.InputPeerClass)
	ReResolve(ctx context.Context, inputPeer tg.InputPeerClass) (tg.InputPeerClass, error)
}

// MediaUploader defines the upload contract for Telegram media files.
type MediaUploader interface {
	FromPath(ctx context.Context, path string) (tg.InputFileClass, error)
}

// ClientInteraction implements MessageInteraction using MTProto TelegramAPI.
type ClientInteraction struct {
	api        TelegramAPI
	logger     *zap.Logger
	reResolver PeerReResolver
	metrics    core.MetricsCollector
	sender     *message.Sender
	uploader   MediaUploader
	executor   assistentrpc.Executor
}

var _ MessageInteraction = (*ClientInteraction)(nil)

// NewClientInteraction creates an interaction engine backed by a Telegram API instance.
func NewClientInteraction(api TelegramAPI, logger *zap.Logger) *ClientInteraction {
	if logger == nil {
		logger = zap.NewNop()
	}
	ci := &ClientInteraction{
		api:      api,
		logger:   logger,
		executor: assistentrpc.DirectExecutor{},
	}
	if tgClient, ok := api.(*tg.Client); ok {
		ci.sender = message.NewSender(tgClient)
		ci.uploader = uploader.NewUploader(tgClient)
	}
	return ci
}

// SetMediaSender sets custom media sender and uploader components.
func (c *ClientInteraction) SetMediaSender(sender *message.Sender, upl MediaUploader) {
	c.sender = sender
	c.uploader = upl
}

// SetRPCExecutor configures the shared executor used for multi-request media operations.
func (c *ClientInteraction) SetRPCExecutor(executor assistentrpc.Executor) {
	if executor != nil {
		c.executor = executor
	}
}

func executeValue[T any](ctx context.Context, executor assistentrpc.Executor, method, family string, kind assistentrpc.Kind, timeout time.Duration, operation func(context.Context) (T, error)) (T, error) {
	var value T
	err := executor.Do(ctx, method, family, kind, timeout, func(opCtx context.Context) error {
		var opErr error
		value, opErr = operation(opCtx)
		return opErr
	})
	return value, err
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
		return plain, sanitizeEntities(plain, ents)
	}
	return text, nil
}

// sanitizeEntities ensures that entities stay within valid UTF-16 bounds and removes
// duplicate MessageEntityCode instances that overlap identically with MessageEntityPre
// (which triggers Telegram rpc 400 ENTITY_BOUNDS_INVALID).
func sanitizeEntities(plain string, ents []tg.MessageEntityClass) []tg.MessageEntityClass {
	if len(ents) == 0 {
		return ents
	}
	u16Len := len(utf16.Encode([]rune(plain)))

	valid := make([]tg.MessageEntityClass, 0, len(ents))
	for _, e := range ents {
		offset := e.GetOffset()
		length := e.GetLength()
		if offset < 0 || length <= 0 || offset+length > u16Len {
			continue
		}
		valid = append(valid, e)
	}

	cleaned := make([]tg.MessageEntityClass, 0, len(valid))
	for _, e := range valid {
		if code, ok := e.(*tg.MessageEntityCode); ok {
			isDuplicate := false
			for _, other := range valid {
				if pre, isPre := other.(*tg.MessageEntityPre); isPre {
					if pre.Offset == code.Offset && pre.Length == code.Length {
						isDuplicate = true
						break
					}
				}
			}
			if isDuplicate {
				continue
			}
		}
		cleaned = append(cleaned, e)
	}
	return cleaned
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
			c.logger.Warn("assistant: access hash stale during edit markup, invalidating and re-resolving",
				zap.Int("msg_id", target.MessageID()),
			)
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
				c.logger.Warn("assistant: access hash stale during get message, invalidating and re-resolving",
					zap.Int("msg_id", target.MessageID()),
				)
				c.reResolver.InvalidatePeer(currentPeer)
				if newPeer, rerr := c.reResolver.ReResolve(ctx, currentPeer); rerr == nil && newPeer != nil {
					currentPeer = newPeer
					continue
				}
			}
			return nil, classified
		}

		extractTargetMessage := func(slice []tg.MessageClass) *tg.Message {
			for _, item := range slice {
				if m, ok := item.(*tg.Message); ok && m.ID == target.MessageID() {
					return m
				}
			}
			return nil
		}

		switch msgs := res.(type) {
		case *tg.MessagesMessages:
			if m := extractTargetMessage(msgs.Messages); m != nil {
				return m, nil
			}
		case *tg.MessagesMessagesSlice:
			if m := extractTargetMessage(msgs.Messages); m != nil {
				return m, nil
			}
		case *tg.MessagesChannelMessages:
			if m := extractTargetMessage(msgs.Messages); m != nil {
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

// ForwardMessageWithRandomID forwards one Telegram message using a caller-owned
// random_id. Callers must durably persist randomID before invoking this method;
// retrying the same logical forward with the same ID is Telegram-idempotent.
func (c *ClientInteraction) ForwardMessageWithRandomID(
	ctx context.Context,
	fromPeer tg.InputPeerClass,
	toPeer tg.InputPeerClass,
	messageID int,
	randomID int64,
) (_ *tg.Message, retErr error) {
	if c == nil || c.api == nil || fromPeer == nil || toPeer == nil || messageID <= 0 || randomID == 0 {
		return nil, ErrInvalidTarget
	}
	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.RecordTelegramRequest("MessagesForwardMessages", time.Since(start), retErr)
		}
	}()

	updates, err := c.api.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		FromPeer: fromPeer,
		ID:       []int{messageID},
		RandomID: []int64{randomID},
		ToPeer:   toPeer,
	})
	if err != nil {
		retErr = fmt.Errorf("assistant forward message: %w", ClassifyRPCError(err))
		return nil, retErr
	}
	msg := extractMessage(updates)
	if msg == nil || msg.ID <= 0 {
		retErr = fmt.Errorf("assistant forward message: Telegram returned no target message")
		return nil, retErr
	}
	return msg, nil
}

// SendMedia uploads and sends media (photo, sticker, audio, video, file) to the specified peer.
func (c *ClientInteraction) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (_ *tg.Message, retErr error) {
	if c.sender == nil || c.uploader == nil {
		return nil, fmt.Errorf("%w: assistant media upload is not configured", core.ErrUnsupported)
	}
	if peer == nil {
		return nil, ErrInvalidTarget
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect file %q: %w", filePath, err)
	}
	if stat.Size() > core.DefaultMaxUploadSize {
		return nil, fmt.Errorf("%w: file size (%d bytes) exceeds maximum upload limit (500MB)", core.ErrMediaTooLarge, stat.Size())
	}

	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.RecordTelegramRequest("SendMedia", time.Since(start), retErr)
		}
	}()

	const transferTimeout = 30 * time.Minute
	inputFile, err := executeValue(ctx, c.executor, "upload.saveFilePart", "upload", assistentrpc.IdempotentMutation, transferTimeout, func(opCtx context.Context) (tg.InputFileClass, error) {
		return c.uploader.FromPath(opCtx, filePath)
	})
	if err != nil {
		retErr = fmt.Errorf("failed to upload file %q: %w", filePath, err)
		return nil, retErr
	}

	builder := c.sender.To(peer)
	var styledCaption []message.StyledTextOption
	if caption != "" {
		styledCaption = append(styledCaption, html.String(nil, caption))
	}

	updates, err := executeValue(ctx, c.executor, "messages.sendMedia", "messages", assistentrpc.NonIdempotentMutation, transferTimeout, func(opCtx context.Context) (tg.UpdatesClass, error) {
		switch mediaType {
		case "photo":
			return builder.UploadedPhoto(opCtx, inputFile, styledCaption...)
		case "sticker":
			return builder.UploadedSticker(opCtx, inputFile, styledCaption...)
		case "audio":
			return builder.Audio(opCtx, inputFile, styledCaption...)
		case "video":
			return builder.Video(opCtx, inputFile, styledCaption...)
		case "file", "document":
			fallthrough
		default:
			return builder.File(opCtx, inputFile, styledCaption...)
		}
	})

	if err != nil {
		retErr = fmt.Errorf("failed to send media (%s): %w", mediaType, ClassifyRPCError(err))
		return nil, retErr
	}

	return extractMessage(updates), nil
}

// UploadInlineMedia uploads media and obtains a reusable Telegram media
// reference without sending a chat message. The caller may use that reference
// in one inline answer.
func (c *ClientInteraction) UploadInlineMedia(
	ctx context.Context,
	mediaType string,
	filePath string,
	fileName string,
	mimeType string,
) (_ tg.MessageMediaClass, retErr error) {
	if c == nil || c.sender == nil || c.uploader == nil {
		return nil, fmt.Errorf("%w: assistant media upload is not configured", core.ErrUnsupported)
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect file %q: %w", filePath, err)
	}
	if stat.Size() > core.DefaultMaxUploadSize {
		return nil, fmt.Errorf("%w: file size (%d bytes) exceeds maximum upload limit (500MB)", core.ErrMediaTooLarge, stat.Size())
	}

	const transferTimeout = 30 * time.Minute
	inputFile, err := executeValue(ctx, c.executor, "upload.saveFilePart", "upload", assistentrpc.IdempotentMutation, transferTimeout, func(opCtx context.Context) (tg.InputFileClass, error) {
		return c.uploader.FromPath(opCtx, filePath)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upload inline media %q: %w", filePath, err)
	}

	var media message.MediaOption
	kind := strings.ToLower(strings.TrimSpace(mediaType))
	if kind == "photo" {
		media = message.UploadedPhoto(inputFile)
	} else {
		doc := message.UploadedDocument(inputFile)
		if mime := strings.TrimSpace(mimeType); mime != "" {
			doc.MIME(mime)
		}
		if name := strings.TrimSpace(fileName); name != "" {
			doc.Filename(name)
		}
		switch kind {
		case "sticker":
			media = doc.UploadedSticker()
		case "audio":
			media = doc.Audio()
		case "video":
			media = doc.Video()
		default:
			media = doc.ForceFile(true)
		}
	}

	return executeValue(ctx, c.executor, "messages.uploadMedia", "messages", assistentrpc.IdempotentMutation, transferTimeout, func(opCtx context.Context) (tg.MessageMediaClass, error) {
		return c.sender.Self().UploadMedia(opCtx, media)
	})
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

// EditMarkup updates only the inline markup of an inline bot message, preserving the text on Telegram.
func (i *InlineClientInteraction) EditMarkup(ctx context.Context, target InlineTarget, markup tg.ReplyMarkupClass) (retErr error) {
	if i.ci == nil || i.ci.api == nil || !target.IsValid() {
		return ErrInvalidTarget
	}
	start := time.Now()
	defer func() {
		if i.ci != nil && i.ci.metrics != nil {
			i.ci.metrics.RecordTelegramRequest("MessagesEditInlineBotMessage", time.Since(start), retErr)
		}
	}()

	req := &tg.MessagesEditInlineBotMessageRequest{
		ID: target.MessageID(),
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
		retErr = fmt.Errorf("assistant edit inline message markup: %w", classified)
		return retErr
	}
	return nil
}
