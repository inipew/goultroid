package interaction

import (
	"context"

	"github.com/gotd/td/tg"
)

// MessageInteraction defines primitives for interacting with standard Telegram dialog messages.
type MessageInteraction interface {
	Answer(ctx context.Context, queryID int64, text string, alert bool) error
	Edit(ctx context.Context, target MessageTarget, text string, markup tg.ReplyMarkupClass) error
	EditMarkup(ctx context.Context, target MessageTarget, markup tg.ReplyMarkupClass) error
	Delete(ctx context.Context, target MessageTarget) error
	GetMessage(ctx context.Context, target MessageTarget) (*tg.Message, error)
	SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error)
}

// InlineInteraction defines primitives for interacting with inline-sent messages.
type InlineInteraction interface {
	Answer(ctx context.Context, queryID int64, text string, alert bool) error
	Edit(ctx context.Context, target InlineTarget, text string, markup tg.ReplyMarkupClass) error
}

// TelegramAPI defines the MTProto RPC method signatures required by Assistant interactions.
// *tg.Client naturally satisfies this interface.
type TelegramAPI interface {
	MessagesSetBotCallbackAnswer(ctx context.Context, req *tg.MessagesSetBotCallbackAnswerRequest) (bool, error)
	MessagesEditMessage(ctx context.Context, req *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error)
	MessagesDeleteMessages(ctx context.Context, req *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	ChannelsDeleteMessages(ctx context.Context, req *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	MessagesGetMessages(ctx context.Context, id []tg.InputMessageClass) (tg.MessagesMessagesClass, error)
	ChannelsGetMessages(ctx context.Context, req *tg.ChannelsGetMessagesRequest) (tg.MessagesMessagesClass, error)
	MessagesSendMessage(ctx context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error)
	MessagesEditInlineBotMessage(ctx context.Context, req *tg.MessagesEditInlineBotMessageRequest) (bool, error)
}
