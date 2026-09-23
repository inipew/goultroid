package command_test

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
)

func dispatchTest(
	r *command.Router,
	ctx context.Context,
	senderID int64,
	peer tg.InputPeerClass,
	messageText string,
	inter interaction.MessageInteraction,
) error {
	return r.DispatchMessageContext(
		ctx,
		senderID,
		peer,
		messageText,
		testMessageContext(senderID, peer, 0, 0),
		inter,
	)
}

func dispatchMessageTest(
	r *command.Router,
	ctx context.Context,
	senderID int64,
	peer tg.InputPeerClass,
	messageText string,
	messageID int,
	replyToMessageID int,
	inter interaction.MessageInteraction,
) error {
	return r.DispatchMessageContext(
		ctx,
		senderID,
		peer,
		messageText,
		testMessageContext(senderID, peer, messageID, replyToMessageID),
		inter,
	)
}

func testMessageContext(senderID int64, peer tg.InputPeerClass, messageID, replyToMessageID int) command.MessageContext {
	chat := core.Chat{}
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		chat.ID = p.UserID
		chat.Type = string(core.ChatKindPrivate)
	case *tg.InputPeerSelf:
		chat.ID = senderID
		chat.Type = string(core.ChatKindPrivate)
	case *tg.InputPeerChat:
		chat.ID = p.ChatID
		chat.Type = string(core.ChatKindGroup)
	case *tg.InputPeerChannel:
		chat.ID = p.ChannelID
		// Tests without Telegram entity metadata intentionally fail closed for
		// group-only behavior, matching the old convenience wrapper.
		chat.Type = string(core.ChatKindChannel)
	}
	if chat.ID == 0 {
		chat.ID = senderID
		chat.Type = string(core.ChatKindPrivate)
	}
	return command.MessageContext{
		Chat:             chat,
		MessageID:        messageID,
		ReplyToMessageID: replyToMessageID,
	}
}
