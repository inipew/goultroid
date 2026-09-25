package telegram

import (
	"context"
	"fmt"
	"os"
	"strings"

	tdcrypto "github.com/gotd/td/crypto"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

var _ core.ContextualTelegramServicer = (*Service)(nil)

func contextualReplyTo(send core.MessageSendContext) tg.InputReplyToClass {
	replyToID := send.ReplyToID
	if replyToID <= 0 {
		replyToID = send.TopicID
	}
	if replyToID <= 0 {
		return nil
	}
	reply := &tg.InputReplyToMessage{ReplyToMsgID: replyToID}
	if send.TopicID > 0 && send.TopicID != replyToID {
		reply.TopMsgID = send.TopicID
	}
	return reply
}

func contextualRandomID() (int64, error) {
	return tdcrypto.RandInt64(tdcrypto.DefaultRand())
}

func contextualUploadedMedia(mediaType string, file tg.InputFileClass) tg.InputMediaClass {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "photo":
		return &tg.InputMediaUploadedPhoto{File: file}
	case "sticker":
		return &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: message.DefaultStickerMIME,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeSticker{Stickerset: &tg.InputStickerSetEmpty{}},
			},
		}
	case "audio":
		return &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: message.DefaultAudioMIME,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeAudio{},
			},
		}
	case "voice":
		return &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: message.DefaultVoiceMIME,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeAudio{Voice: true},
			},
		}
	case "video":
		return &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: message.DefaultVideoMIME,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeVideo{},
			},
		}
	default:
		return &tg.InputMediaUploadedDocument{
			File:      file,
			MimeType:  "application/octet-stream",
			ForceFile: true,
		}
	}
}

// SendMessageContext sends text through the same shared RPC executor as normal
// userbot messages while preserving both the direct reply target and forum root.
func (s *Service) SendMessageContext(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
	markup tg.ReplyMarkupClass,
	send core.MessageSendContext,
) (*tg.Message, error) {
	if s == nil || s.api == nil {
		return nil, fmt.Errorf("%w: telegram api is not initialized", core.ErrInternal)
	}
	if peer == nil {
		return nil, fmt.Errorf("%w: peer is nil", core.ErrInvalidArgs)
	}

	randomID, err := contextualRandomID()
	if err != nil {
		return nil, fmt.Errorf("generate message random id: %w", err)
	}
	plain, entities := parseHTML(text)
	res, err := s.execNonIdempotentPeerVal(ctx, "messages.sendMessage", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (*tg.Message, error) {
		req := &tg.MessagesSendMessageRequest{
			Peer:     currentPeer,
			Message:  plain,
			RandomID: randomID,
		}
		if len(entities) > 0 {
			req.SetEntities(entities)
		}
		if markup != nil {
			req.SetReplyMarkup(markup)
		}
		if reply := contextualReplyTo(send); reply != nil {
			req.SetReplyTo(reply)
		}
		updates, invokeErr := s.api.MessagesSendMessage(opCtx, req)
		if invokeErr != nil {
			return nil, invokeErr
		}
		return extractMessageFromUpdates(updates), nil
	})
	if err != nil {
		s.checkPeerError(ctx, err, peer)
		return nil, err
	}
	if res != nil {
		s.recordBotSent(peer, res.ID)
	}
	return res, nil
}

// SendMediaContext mirrors SendMedia but supplies Telegram's explicit forum
// reply coordinates on the final send. Upload chunking still uses the existing
// managed uploader and the final mutation still uses the shared RPC executor.
func (s *Service) SendMediaContext(
	ctx context.Context,
	peer tg.InputPeerClass,
	mediaType string,
	filePath string,
	caption string,
	send core.MessageSendContext,
) (*tg.Message, error) {
	if s == nil || s.api == nil || s.uploader == nil {
		return nil, fmt.Errorf("%w: sender/uploader is not initialized", core.ErrInternal)
	}
	if peer == nil {
		return nil, fmt.Errorf("%w: peer is nil", core.ErrInvalidArgs)
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect file %q: %w", filePath, err)
	}
	if stat.Size() > core.DefaultMaxUploadSize {
		return nil, fmt.Errorf("%w: file size (%d bytes) exceeds maximum upload limit (500MB)", core.ErrMediaTooLarge, stat.Size())
	}

	transferCtx, cancel := core.WithDefaultTimeout(ctx, mediaTransferTimeout)
	inputFile, err := s.uploader.FromPath(transferCtx, filePath)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("failed to upload file %q: %w", filePath, unwrapMediaRPCBoundary(err))
	}

	randomID, err := contextualRandomID()
	if err != nil {
		return nil, fmt.Errorf("generate media random id: %w", err)
	}
	plain, entities := parseHTML(caption)
	updates, err := s.execNonIdempotentPeerVal(ctx, "messages.sendMedia", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (tg.UpdatesClass, error) {
		req := &tg.MessagesSendMediaRequest{
			Peer:     currentPeer,
			Media:    contextualUploadedMedia(mediaType, inputFile),
			Message:  plain,
			RandomID: randomID,
		}
		if len(entities) > 0 {
			req.SetEntities(entities)
		}
		if reply := contextualReplyTo(send); reply != nil {
			req.SetReplyTo(reply)
		}
		return s.api.MessagesSendMedia(opCtx, req)
	})
	if err != nil {
		s.checkPeerError(ctx, err, peer)
		return nil, fmt.Errorf("failed to send media (%s): %w", mediaType, err)
	}

	msg := extractMessageFromUpdates(updates)
	if msg != nil {
		s.recordBotSent(peer, msg.ID)
	}
	return msg, nil
}
