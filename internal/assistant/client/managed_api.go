package client

import (
	"context"
	"strings"

	"github.com/gotd/td/tg"
	assistentrpc "github.com/inipew/goultroid/internal/assistant/rpc"
)

type managedAPI struct {
	raw      *tg.Client
	executor assistentrpc.Executor
}

func managedValue[T any](ctx context.Context, a *managedAPI, method string, kind assistentrpc.Kind, op func(context.Context) (T, error)) (T, error) {
	var value T
	family, _, _ := strings.Cut(method, ".")
	err := a.executor.Do(ctx, method, family, kind, 0, func(opCtx context.Context) error {
		var opErr error
		value, opErr = op(opCtx)
		return opErr
	})
	return value, err
}

func (a *managedAPI) MessagesSetBotCallbackAnswer(ctx context.Context, req *tg.MessagesSetBotCallbackAnswerRequest) (bool, error) {
	return managedValue(ctx, a, "messages.setBotCallbackAnswer", assistentrpc.IdempotentMutation, func(opCtx context.Context) (bool, error) { return a.raw.MessagesSetBotCallbackAnswer(opCtx, req) })
}
func (a *managedAPI) MessagesEditMessage(ctx context.Context, req *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error) {
	return managedValue(ctx, a, "messages.editMessage", assistentrpc.IdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) { return a.raw.MessagesEditMessage(opCtx, req) })
}
func (a *managedAPI) MessagesDeleteMessages(ctx context.Context, req *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	return managedValue(ctx, a, "messages.deleteMessages", assistentrpc.IdempotentMutation, func(opCtx context.Context) (*tg.MessagesAffectedMessages, error) {
		return a.raw.MessagesDeleteMessages(opCtx, req)
	})
}
func (a *managedAPI) ChannelsDeleteMessages(ctx context.Context, req *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	return managedValue(ctx, a, "channels.deleteMessages", assistentrpc.IdempotentMutation, func(opCtx context.Context) (*tg.MessagesAffectedMessages, error) {
		return a.raw.ChannelsDeleteMessages(opCtx, req)
	})
}
func (a *managedAPI) MessagesGetMessages(ctx context.Context, id []tg.InputMessageClass) (tg.MessagesMessagesClass, error) {
	return managedValue(ctx, a, "messages.getMessages", assistentrpc.ReadOnly, func(opCtx context.Context) (tg.MessagesMessagesClass, error) {
		return a.raw.MessagesGetMessages(opCtx, id)
	})
}
func (a *managedAPI) ChannelsGetMessages(ctx context.Context, req *tg.ChannelsGetMessagesRequest) (tg.MessagesMessagesClass, error) {
	return managedValue(ctx, a, "channels.getMessages", assistentrpc.ReadOnly, func(opCtx context.Context) (tg.MessagesMessagesClass, error) {
		return a.raw.ChannelsGetMessages(opCtx, req)
	})
}
func (a *managedAPI) MessagesSendMessage(ctx context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	return managedValue(ctx, a, "messages.sendMessage", assistentrpc.NonIdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) { return a.raw.MessagesSendMessage(opCtx, req) })
}
func (a *managedAPI) MessagesSendMessageDurable(ctx context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	// The caller persists req.RandomID before transport, so retries are the same
	// logical Telegram send rather than a new bot-authored message.
	return managedValue(ctx, a, "messages.sendMessage", assistentrpc.IdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) {
		return a.raw.MessagesSendMessage(opCtx, req)
	})
}
func (a *managedAPI) MessagesSendMedia(ctx context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
	return managedValue(ctx, a, "messages.sendMedia", assistentrpc.NonIdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) {
		return a.raw.MessagesSendMedia(opCtx, req)
	})
}
func (a *managedAPI) MessagesSendMediaDurable(ctx context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
	// PM Relay persists req.RandomID before transport, so the server-side media
	// copy may use the idempotent mutation lane without risking duplicate sends.
	return managedValue(ctx, a, "messages.sendMedia", assistentrpc.IdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) {
		return a.raw.MessagesSendMedia(opCtx, req)
	})
}
func (a *managedAPI) MessagesForwardMessages(ctx context.Context, req *tg.MessagesForwardMessagesRequest) (tg.UpdatesClass, error) {
	// PM Relay persists the request random_id before transport. Repeating the
	// same request is therefore one logical Telegram mutation and may use the
	// idempotent retry lane safely.
	return managedValue(ctx, a, "messages.forwardMessages", assistentrpc.IdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) {
		return a.raw.MessagesForwardMessages(opCtx, req)
	})
}
func (a *managedAPI) MessagesEditInlineBotMessage(ctx context.Context, req *tg.MessagesEditInlineBotMessageRequest) (bool, error) {
	return managedValue(ctx, a, "messages.editInlineBotMessage", assistentrpc.IdempotentMutation, func(opCtx context.Context) (bool, error) { return a.raw.MessagesEditInlineBotMessage(opCtx, req) })
}
func (a *managedAPI) UsersGetUsers(ctx context.Context, id []tg.InputUserClass) ([]tg.UserClass, error) {
	return managedValue(ctx, a, "users.getUsers", assistentrpc.ReadOnly, func(opCtx context.Context) ([]tg.UserClass, error) { return a.raw.UsersGetUsers(opCtx, id) })
}
func (a *managedAPI) ChannelsGetChannels(ctx context.Context, id []tg.InputChannelClass) (tg.MessagesChatsClass, error) {
	return managedValue(ctx, a, "channels.getChannels", assistentrpc.ReadOnly, func(opCtx context.Context) (tg.MessagesChatsClass, error) {
		return a.raw.ChannelsGetChannels(opCtx, id)
	})
}
func (a *managedAPI) MessagesSetInlineBotResults(ctx context.Context, req *tg.MessagesSetInlineBotResultsRequest) (bool, error) {
	return managedValue(ctx, a, "messages.setInlineBotResults", assistentrpc.IdempotentMutation, func(opCtx context.Context) (bool, error) { return a.raw.MessagesSetInlineBotResults(opCtx, req) })
}
func (a *managedAPI) BotsSetBotCommands(ctx context.Context, req *tg.BotsSetBotCommandsRequest) (bool, error) {
	return managedValue(ctx, a, "bots.setBotCommands", assistentrpc.IdempotentMutation, func(opCtx context.Context) (bool, error) { return a.raw.BotsSetBotCommands(opCtx, req) })
}

func (a *managedAPI) ContactsResolveUsername(ctx context.Context, req *tg.ContactsResolveUsernameRequest) (*tg.ContactsResolvedPeer, error) {
	return managedValue(ctx, a, "contacts.resolveUsername", assistentrpc.ReadOnly, func(opCtx context.Context) (*tg.ContactsResolvedPeer, error) {
		return a.raw.ContactsResolveUsername(opCtx, req)
	})
}

func (a *managedAPI) ChannelsGetParticipant(ctx context.Context, req *tg.ChannelsGetParticipantRequest) (*tg.ChannelsChannelParticipant, error) {
	return managedValue(ctx, a, "channels.getParticipant", assistentrpc.ReadOnly, func(opCtx context.Context) (*tg.ChannelsChannelParticipant, error) {
		return a.raw.ChannelsGetParticipant(opCtx, req)
	})
}

func (a *managedAPI) ChannelsGetFullChannel(ctx context.Context, channel tg.InputChannelClass) (*tg.MessagesChatFull, error) {
	return managedValue(ctx, a, "channels.getFullChannel", assistentrpc.ReadOnly, func(opCtx context.Context) (*tg.MessagesChatFull, error) {
		return a.raw.ChannelsGetFullChannel(opCtx, channel)
	})
}

func (a *managedAPI) MessagesGetFullChat(ctx context.Context, chatID int64) (*tg.MessagesChatFull, error) {
	return managedValue(ctx, a, "messages.getFullChat", assistentrpc.ReadOnly, func(opCtx context.Context) (*tg.MessagesChatFull, error) {
		return a.raw.MessagesGetFullChat(opCtx, chatID)
	})
}

func (a *managedAPI) ChannelsEditBanned(ctx context.Context, req *tg.ChannelsEditBannedRequest) (tg.UpdatesClass, error) {
	return managedValue(ctx, a, "channels.editBanned", assistentrpc.IdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) {
		return a.raw.ChannelsEditBanned(opCtx, req)
	})
}

func (a *managedAPI) ChannelsEditAdmin(ctx context.Context, req *tg.ChannelsEditAdminRequest) (tg.UpdatesClass, error) {
	return managedValue(ctx, a, "channels.editAdmin", assistentrpc.IdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) {
		return a.raw.ChannelsEditAdmin(opCtx, req)
	})
}

func (a *managedAPI) MessagesDeleteChatUser(ctx context.Context, req *tg.MessagesDeleteChatUserRequest) (tg.UpdatesClass, error) {
	return managedValue(ctx, a, "messages.deleteChatUser", assistentrpc.IdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) {
		return a.raw.MessagesDeleteChatUser(opCtx, req)
	})
}

func (a *managedAPI) MessagesEditChatAdmin(ctx context.Context, req *tg.MessagesEditChatAdminRequest) (bool, error) {
	return managedValue(ctx, a, "messages.editChatAdmin", assistentrpc.IdempotentMutation, func(opCtx context.Context) (bool, error) {
		return a.raw.MessagesEditChatAdmin(opCtx, req)
	})
}

func (a *managedAPI) MessagesUpdatePinnedMessage(ctx context.Context, req *tg.MessagesUpdatePinnedMessageRequest) (tg.UpdatesClass, error) {
	return managedValue(ctx, a, "messages.updatePinnedMessage", assistentrpc.IdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) {
		return a.raw.MessagesUpdatePinnedMessage(opCtx, req)
	})
}

func (a *managedAPI) MessagesEditChatDefaultBannedRights(ctx context.Context, req *tg.MessagesEditChatDefaultBannedRightsRequest) (tg.UpdatesClass, error) {
	return managedValue(ctx, a, "messages.editChatDefaultBannedRights", assistentrpc.IdempotentMutation, func(opCtx context.Context) (tg.UpdatesClass, error) {
		return a.raw.MessagesEditChatDefaultBannedRights(opCtx, req)
	})
}

func (a *managedAPI) MessagesGetHistory(ctx context.Context, req *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error) {
	return managedValue(ctx, a, "messages.getHistory", assistentrpc.ReadOnly, func(opCtx context.Context) (tg.MessagesMessagesClass, error) {
		return a.raw.MessagesGetHistory(opCtx, req)
	})
}

func (a *managedAPI) MessagesGetReplies(ctx context.Context, req *tg.MessagesGetRepliesRequest) (tg.MessagesMessagesClass, error) {
	return managedValue(ctx, a, "messages.getReplies", assistentrpc.ReadOnly, func(opCtx context.Context) (tg.MessagesMessagesClass, error) {
		return a.raw.MessagesGetReplies(opCtx, req)
	})
}
