package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	corecallback "github.com/inipew/goultroid/internal/services/callback"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
)

// CoreCallbackDispatcher admits callback query events into the canonical
// callback pipeline and returns an opaque prepared execution lease.
type CoreCallbackDispatcher interface {
	Prepare(
		context.Context,
		*core.CallbackQueryEvent,
		core.TelegramServicer,
		func(string) (tasks.ScopeIdentity, bool),
	) (corecallback.PreparedCallback, error)
}

type inlineQueryAPI interface {
	MessagesSetInlineBotResults(context.Context, *tg.MessagesSetInlineBotResultsRequest) (bool, error)
}

type assistantInlineQueryServicer struct {
	unsupportedTelegramServicer
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
	uploaded, err := s.interaction.UploadInlineMedia(
		ctx,
		media.MediaType,
		media.Path,
		media.FileName,
		media.MIMEType,
	)
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

// unsupportedTelegramServicer implements core.TelegramServicer by returning core.ErrUnsupported
// for all unhandled operations to prevent false successes.
type unsupportedTelegramServicer struct{}

var _ core.TelegramServicer = (*unsupportedTelegramServicer)(nil)

func (u *unsupportedTelegramServicer) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	return nil, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	return nil, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) EditInlineBotMessageMarkup(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) AnswerInlineQueryOptions(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, opts core.InlineAnswerOptions) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	return nil, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return 0, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return nil, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return nil, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	return nil, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return nil, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) UpdateProfile(ctx context.Context, firstName, lastName, about *string) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) BlockUser(ctx context.Context, peer tg.InputPeerClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) UnblockUser(ctx context.Context, peer tg.InputPeerClass) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) UploadProfilePhoto(ctx context.Context, filePath string) error {
	return core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) DeleteProfilePhotos(ctx context.Context, limit int) (int, error) {
	return 0, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) GetDialogs(ctx context.Context, limit int) ([]*core.Chat, error) {
	return nil, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) GetContacts(ctx context.Context) ([]*core.User, error) {
	return nil, core.ErrUnsupported
}
func (u *unsupportedTelegramServicer) IsBotSent(msgID int) bool {
	return false
}

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
	unsupportedTelegramServicer
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
	unsupportedTelegramServicer
	queryID     int64
	target      interaction.InlineTarget
	interaction interaction.InlineInteraction
	answer      callbackAnswerGuard
}

func newAssistantInlineCallbackServicer(queryID int64, target interaction.InlineTarget, inter interaction.InlineInteraction) *assistantInlineCallbackServicer {
	return &assistantInlineCallbackServicer{queryID: queryID, target: target, interaction: inter}
}

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
