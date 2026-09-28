package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
)

type inlineQueryAPI interface {
	MessagesSetInlineBotResults(context.Context, *tg.MessagesSetInlineBotResultsRequest) (bool, error)
}

type assistantInlineQueryServicer struct {
	api         inlineQueryAPI
	interaction *interaction.ClientInteraction
}

func newAssistantInlineQueryServicer(api inlineQueryAPI, inter *interaction.ClientInteraction) *assistantInlineQueryServicer {
	return &assistantInlineQueryServicer{api: api, interaction: inter}
}

func (s *assistantInlineQueryServicer) AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error {
	return s.AnswerInlineQueryOptions(ctx, queryID, results, core.InlineAnswerOptions{NextOffset: nextOffset, CacheTime: cacheTime})
}

func (s *assistantInlineQueryServicer) AnswerInlineQueryOptions(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, opts core.InlineAnswerOptions) error {
	if s == nil || s.api == nil {
		return core.ErrInternal
	}
	if results == nil {
		results = opts.Results
	}
	req := &tg.MessagesSetInlineBotResultsRequest{QueryID: queryID, Results: results, CacheTime: opts.CacheTime, NextOffset: opts.NextOffset, Gallery: opts.Gallery, Private: opts.Private}
	if opts.SwitchPM != nil {
		req.SwitchPm = *opts.SwitchPM
	}
	if opts.SwitchWebView != nil {
		req.SwitchWebview = *opts.SwitchWebView
	}
	req.SetFlags()
	_, err := s.api.MessagesSetInlineBotResults(ctx, req)
	return err
}

func (s *assistantInlineQueryServicer) PrepareInlineLocalMedia(
	ctx context.Context,
	media inlineservice.LocalMedia,
) (inlineservice.PreparedLocalMedia, error) {
	if s == nil || s.interaction == nil {
		return inlineservice.PreparedLocalMedia{}, fmt.Errorf("assistant inline media transport is unavailable")
	}
	uploaded, err := s.interaction.UploadInlineMedia(ctx, presentation.Media{
		Type:     media.MediaType,
		Path:     media.Path,
		FileName: media.FileName,
		MIMEType: media.MIMEType,
	})
	if err != nil {
		return inlineservice.PreparedLocalMedia{}, err
	}

	switch value := uploaded.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := value.Photo.AsNotEmpty()
		if !ok {
			return inlineservice.PreparedLocalMedia{}, fmt.Errorf("assistant inline upload returned empty photo")
		}
		return inlineservice.PreparedLocalMedia{Photo: photo.AsInput()}, nil
	case *tg.MessageMediaDocument:
		document, ok := value.Document.AsNotEmpty()
		if !ok {
			return inlineservice.PreparedLocalMedia{}, fmt.Errorf("assistant inline upload returned empty document")
		}
		return inlineservice.PreparedLocalMedia{Document: document.AsInput()}, nil
	default:
		return inlineservice.PreparedLocalMedia{}, fmt.Errorf("assistant inline upload returned unsupported media %T", uploaded)
	}
}

func resolveMessageTarget(base interaction.MessageTarget, peer tg.InputPeerClass, msgID int) interaction.MessageTarget {
	tPeer := base.Peer()
	if peer != nil {
		tPeer = peer
	}
	tMsgID := base.MessageID()
	if msgID > 0 {
		tMsgID = msgID
	}
	chatID := base.ChatID()
	if peer != nil {
		if cID := extractChatIDFromInputPeer(peer); cID != 0 {
			chatID = cID
		}
	}
	return interaction.NewMessageTarget(tPeer, tMsgID, chatID, base.ChatInstance())
}

var _ inlineservice.TelegramAnswerer = (*assistantInlineQueryServicer)(nil)

type callbackAnswerGuard struct {
	mu        sync.Mutex
	answering bool
	answered  bool
}

func (g *callbackAnswerGuard) Do(answer func() error) error {
	if g == nil || answer == nil {
		return core.ErrInternal
	}
	g.mu.Lock()
	if g.answered || g.answering {
		g.mu.Unlock()
		return interaction.ErrCallbackAlreadyAnswered
	}
	g.answering = true
	g.mu.Unlock()

	err := answer()

	g.mu.Lock()
	g.answering = false
	if err == nil {
		g.answered = true
	}
	g.mu.Unlock()
	return err
}

// assistantCallbackServicer is the narrow Telegram transport adapter used by
// canonical core/plugin callback handlers for message-origin callbacks.
type assistantCallbackServicer struct {
	queryID     int64
	target      interaction.MessageTarget
	interaction interaction.MessageInteraction
	answer      callbackAnswerGuard
}

func newAssistantCallbackServicer(queryID int64, target interaction.MessageTarget, inter interaction.MessageInteraction) *assistantCallbackServicer {
	return &assistantCallbackServicer{queryID: queryID, target: target, interaction: inter}
}

func (s *assistantCallbackServicer) AnswerCallbackQuery(ctx context.Context, _ int64, text string, alert bool) error {
	if s == nil || s.interaction == nil || s.queryID == 0 {
		return core.ErrInternal
	}
	return s.answer.Do(func() error {
		return s.interaction.Answer(ctx, s.queryID, text, alert)
	})
}

func (s *assistantCallbackServicer) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if s == nil || s.interaction == nil {
		return core.ErrInternal
	}
	target := resolveMessageTarget(s.target, peer, msgID)
	return s.interaction.Edit(ctx, target, text, markup)
}

func (s *assistantCallbackServicer) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	if s == nil || s.interaction == nil {
		return core.ErrInternal
	}
	target := resolveMessageTarget(s.target, peer, msgID)
	return s.interaction.EditMarkup(ctx, target, markup)
}

func (s *assistantCallbackServicer) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return s.EditMessageMarkup(ctx, peer, msgID, text, nil)
}

func (s *assistantCallbackServicer) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if s == nil || s.interaction == nil {
		return core.ErrInternal
	}
	if len(msgIDs) == 0 {
		return s.interaction.Delete(ctx, resolveMessageTarget(s.target, peer, 0))
	}
	var firstErr error
	for _, id := range msgIDs {
		if err := s.interaction.Delete(ctx, resolveMessageTarget(s.target, peer, id)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *assistantCallbackServicer) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if s == nil || s.interaction == nil {
		return nil, core.ErrInternal
	}
	return s.interaction.GetMessage(ctx, resolveMessageTarget(s.target, peer, msgID))
}

func (s *assistantCallbackServicer) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	return s.SendMessageWithMarkup(ctx, peer, text, nil)
}

func (s *assistantCallbackServicer) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if s == nil || s.interaction == nil {
		return nil, core.ErrInternal
	}
	if peer == nil {
		peer = s.target.Peer()
	}
	return s.interaction.SendMessage(ctx, peer, text, markup)
}

func (s *assistantCallbackServicer) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	if s == nil || s.interaction == nil {
		return nil, core.ErrInternal
	}
	if peer == nil {
		peer = s.target.Peer()
	}
	return s.interaction.SendMedia(ctx, peer, mediaType, filePath, caption)
}

// assistantInlineCallbackServicer is the narrow Telegram transport adapter used
// by canonical core/plugin callback handlers for inline-origin callbacks.
type assistantInlineCallbackServicer struct {
	queryID     int64
	target      interaction.InlineTarget
	interaction interaction.InlineInteraction
	answer      callbackAnswerGuard
}

type assistantMessageCallbackTransport interface {
	AnswerCallbackQuery(context.Context, int64, string, bool) error
	EditMessage(context.Context, tg.InputPeerClass, int, string) error
	EditMessageMarkup(context.Context, tg.InputPeerClass, int, string, tg.ReplyMarkupClass) error
	EditMessageMarkupOnly(context.Context, tg.InputPeerClass, int, tg.ReplyMarkupClass) error
	DeleteMessage(context.Context, tg.InputPeerClass, []int) error
	GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error)
	SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error)
	SendMessageWithMarkup(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass) (*tg.Message, error)
	SendMedia(context.Context, tg.InputPeerClass, string, string, string) (*tg.Message, error)
}

type assistantInlineCallbackTransport interface {
	AnswerCallbackQuery(context.Context, int64, string, bool) error
	EditInlineBotMessage(context.Context, tg.InputBotInlineMessageIDClass, string, tg.ReplyMarkupClass) error
	EditInlineBotMessageMarkup(context.Context, tg.InputBotInlineMessageIDClass, tg.ReplyMarkupClass) error
}

var (
	_ assistantMessageCallbackTransport = (*assistantCallbackServicer)(nil)
	_ assistantInlineCallbackTransport  = (*assistantInlineCallbackServicer)(nil)
)

func (s *assistantInlineCallbackServicer) AnswerCallbackQuery(ctx context.Context, _ int64, text string, alert bool) error {
	if s == nil || s.interaction == nil || s.queryID == 0 {
		return core.ErrInternal
	}
	return s.answer.Do(func() error {
		return s.interaction.Answer(ctx, s.queryID, text, alert)
	})
}

func (s *assistantInlineCallbackServicer) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	if s == nil || s.interaction == nil {
		return core.ErrInternal
	}
	target := s.target
	if inlineID != nil {
		target = interaction.NewInlineTarget(target.QueryID(), inlineID, target.ChatInstance())
	}
	return s.interaction.Edit(ctx, target, text, markup)
}

func (s *assistantInlineCallbackServicer) EditInlineBotMessageMarkup(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error {
	if s == nil || s.interaction == nil {
		return core.ErrInternal
	}
	target := s.target
	if inlineID != nil {
		target = interaction.NewInlineTarget(target.QueryID(), inlineID, target.ChatInstance())
	}
	return s.interaction.EditMarkup(ctx, target, markup)
}

func extractChatIDFromInputPeer(peer tg.InputPeerClass) int64 {
	if peer == nil {
		return 0
	}
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return p.UserID
	case *tg.InputPeerChat:
		return p.ChatID
	case *tg.InputPeerChannel:
		return p.ChannelID
	default:
		return 0
	}
}
