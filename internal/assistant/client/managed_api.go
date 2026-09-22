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
