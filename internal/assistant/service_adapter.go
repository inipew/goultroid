package assistant

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
	"strings"
)

// BotServiceAdapter adapts a bot MTProto tg.Client to the core.TelegramServicer interface.
// This enables callback.Router and inline.Engine to answer queries and edit messages using
// the Assistant Bot session.
type BotServiceAdapter struct {
	api    *tg.Client
	logger *zap.Logger
}

var _ core.TelegramServicer = (*BotServiceAdapter)(nil)

// NewBotServiceAdapter creates a new BotServiceAdapter around an active MTProto API client.
func NewBotServiceAdapter(api *tg.Client, logger *zap.Logger) *BotServiceAdapter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BotServiceAdapter{
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

func extractMessageFromUpdates(u tg.UpdatesClass) *tg.Message {
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

// SendMessage sends a plain or HTML formatted message using the Bot account.
func (a *BotServiceAdapter) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	return a.SendMessageWithMarkup(ctx, peer, text, nil)
}

// SendMessageWithMarkup sends a message with inline reply markup using the Bot account.
func (a *BotServiceAdapter) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if a.api == nil {
		return nil, core.ErrInternal
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
	updates, err := a.api.MessagesSendMessage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("assistant bot SendMessageWithMarkup: %w", err)
	}
	return extractMessageFromUpdates(updates), nil
}

// EditMessage edits an existing message text sent by the bot.
func (a *BotServiceAdapter) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return a.EditMessageMarkup(ctx, peer, msgID, text, nil)
}

// EditMessageMarkup edits both text and inline markup of a message sent by the bot.
func (a *BotServiceAdapter) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if a.api == nil {
		return core.ErrInternal
	}
	plain, ents := parseHTML(text)
	req := &tg.MessagesEditMessageRequest{
		Peer: peer,
		ID:   msgID,
	}
	req.SetMessage(plain)
	if len(ents) > 0 {
		req.SetEntities(ents)
	}
	req.SetNoWebpage(true)
	if markup != nil {
		req.SetReplyMarkup(markup)
	}
	_, err := a.api.MessagesEditMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("assistant bot EditMessageMarkup: %w", err)
	}
	return nil
}

// EditMessageMarkupOnly updates only the reply markup of an existing bot message.
func (a *BotServiceAdapter) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	if a.api == nil {
		return core.ErrInternal
	}
	req := &tg.MessagesEditMessageRequest{
		Peer: peer,
		ID:   msgID,
	}
	if markup != nil {
		req.SetReplyMarkup(markup)
	}
	_, err := a.api.MessagesEditMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("assistant bot EditMessageMarkupOnly: %w", err)
	}
	return nil
}

// EditInlineBotMessage edits text and markup of an inline bot message.
func (a *BotServiceAdapter) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	if a.api == nil {
		return core.ErrInternal
	}
	plain, ents := parseHTML(text)
	req := &tg.MessagesEditInlineBotMessageRequest{
		ID: inlineID,
	}
	req.SetMessage(plain)
	if len(ents) > 0 {
		req.SetEntities(ents)
	}
	req.SetNoWebpage(true)
	if markup != nil {
		req.SetReplyMarkup(markup)
	}
	_, err := a.api.MessagesEditInlineBotMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("assistant bot EditInlineBotMessage: %w", err)
	}
	return nil
}

// EditInlineBotMessageMarkup updates only markup of an inline bot message.
func (a *BotServiceAdapter) EditInlineBotMessageMarkup(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error {
	if a.api == nil {
		return core.ErrInternal
	}
	req := &tg.MessagesEditInlineBotMessageRequest{
		ID: inlineID,
	}
	if markup != nil {
		req.SetReplyMarkup(markup)
	}
	_, err := a.api.MessagesEditInlineBotMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("assistant bot EditInlineBotMessageMarkup: %w", err)
	}
	return nil
}

// DeleteMessage deletes messages by ID using peer-aware MTProto RPC with idempotent semantics.
func (a *BotServiceAdapter) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if a.api == nil {
		return core.ErrInternal
	}
	if len(msgIDs) == 0 {
		return nil
	}

	var err error
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		_, err = a.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
			ID:      msgIDs,
		})
	default:
		req := &tg.MessagesDeleteMessagesRequest{ID: msgIDs}
		req.SetRevoke(true)
		_, err = a.api.MessagesDeleteMessages(ctx, req)
	}

	if err != nil {
		if strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
			return nil // Idempotent: message already deleted
		}
		return fmt.Errorf("assistant bot DeleteMessage: %w", err)
	}
	return nil
}

// AnswerCallbackQuery answers an incoming bot callback query popup or toast.
func (a *BotServiceAdapter) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	if a.api == nil {
		return core.ErrInternal
	}
	req := &tg.MessagesSetBotCallbackAnswerRequest{
		QueryID: queryID,
		Alert:   alert,
	}
	if text != "" {
		req.SetMessage(text)
	}
	_, err := a.api.MessagesSetBotCallbackAnswer(ctx, req)
	if err != nil {
		return fmt.Errorf("assistant bot AnswerCallbackQuery: %w", err)
	}
	return nil
}

// AnswerInlineQuery answers an incoming inline query search request.
func (a *BotServiceAdapter) AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error {
	return a.AnswerInlineQueryOptions(ctx, queryID, results, core.InlineAnswerOptions{
		NextOffset: nextOffset,
		CacheTime:  cacheTime,
	})
}

// AnswerInlineQueryOptions answers an inline query with extended options.
func (a *BotServiceAdapter) AnswerInlineQueryOptions(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, opts core.InlineAnswerOptions) error {
	if a.api == nil {
		return core.ErrInternal
	}
	req := &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		Results:   results,
		CacheTime: opts.CacheTime,
		Gallery:   opts.Gallery,
		Private:   opts.Private,
	}
	if opts.NextOffset != "" {
		req.SetNextOffset(opts.NextOffset)
	}
	if opts.SwitchPM != nil {
		req.SetSwitchPm(*opts.SwitchPM)
	}
	_, err := a.api.MessagesSetInlineBotResults(ctx, req)
	if err != nil {
		return fmt.Errorf("assistant bot AnswerInlineQueryOptions: %w", err)
	}
	return nil
}

// GetDialogs is not supported on bot service adapter.
func (a *BotServiceAdapter) GetDialogs(ctx context.Context, limit int) ([]*core.Chat, error) {
	return nil, core.ErrUnsupported
}

// React sends an emoji reaction to a message.
func (a *BotServiceAdapter) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	if a.api == nil {
		return core.ErrInternal
	}
	_, err := a.api.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{
		Peer:  peer,
		MsgID: msgID,
		Reaction: []tg.ReactionClass{
			&tg.ReactionEmoji{Emoticon: emoji},
		},
	})
	return err
}

// GetMessage fetches a single message by ID using peer-aware MTProto RPC.
func (a *BotServiceAdapter) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if a.api == nil {
		return nil, core.ErrInternal
	}

	var res tg.MessagesMessagesClass
	var err error

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		res, err = a.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
		})
	default:
		res, err = a.api.MessagesGetMessages(ctx, []tg.InputMessageClass{
			&tg.InputMessageID{ID: msgID},
		})
	}

	if err != nil {
		return nil, err
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
	return nil, core.ErrNotFound
}

// PinMessage pins a message in chat.
func (a *BotServiceAdapter) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	if a.api == nil {
		return core.ErrInternal
	}
	_, err := a.api.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{
		Silent: silent,
		Peer:   peer,
		ID:     msgID,
	})
	return err
}

// UnpinMessage unpins a message in chat.
func (a *BotServiceAdapter) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	if a.api == nil {
		return core.ErrInternal
	}
	_, err := a.api.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{
		Unpin: true,
		Peer:  peer,
		ID:    msgID,
	})
	return err
}

// ForwardMessages forwards messages to another peer.
func (a *BotServiceAdapter) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	if a.api == nil {
		return core.ErrInternal
	}
	randIDs := make([]int64, len(msgIDs))
	for i := range randIDs {
		randIDs[i] = randomID()
	}
	_, err := a.api.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		FromPeer: fromPeer,
		ToPeer:   toPeer,
		ID:       msgIDs,
		RandomID: randIDs,
	})
	return err
}

// DownloadFile is not supported on bot service adapter directly.
func (a *BotServiceAdapter) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	return fmt.Errorf("download file: %w", core.ErrUnsupported)
}

// SendMedia is not implemented directly on BotServiceAdapter.
func (a *BotServiceAdapter) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return nil, fmt.Errorf("send media: %w", core.ErrUnsupported)
}

// Moderation methods unsupported for assistant bot (userbot owns admin enforcement).
func (a *BotServiceAdapter) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return 0, core.ErrUnsupported
}

func (a *BotServiceAdapter) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return nil, core.ErrUnsupported
}

func (a *BotServiceAdapter) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	if a.api == nil {
		return nil, core.ErrInternal
	}
	return a.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
}

func (a *BotServiceAdapter) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return nil, core.ErrUnsupported
}

func (a *BotServiceAdapter) UpdateProfile(ctx context.Context, firstName, lastName, about *string) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) BlockUser(ctx context.Context, peer tg.InputPeerClass) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) UnblockUser(ctx context.Context, peer tg.InputPeerClass) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) UploadProfilePhoto(ctx context.Context, filePath string) error {
	return core.ErrUnsupported
}

func (a *BotServiceAdapter) DeleteProfilePhotos(ctx context.Context, limit int) (int, error) {
	return 0, core.ErrUnsupported
}

func (a *BotServiceAdapter) GetContacts(ctx context.Context) ([]*core.User, error) {
	return nil, core.ErrUnsupported
}

func (a *BotServiceAdapter) IsBotSent(msgID int) bool {
	return true
}


