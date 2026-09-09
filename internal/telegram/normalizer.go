package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// Normalizer converts raw MTProto updates into normalized internal core.Event objects.
type Normalizer struct{}

// NewNormalizer creates a new Normalizer instance.
func NewNormalizer() *Normalizer {
	return &Normalizer{}
}

// Normalize parses an MTProto update and maps it to a canonical core.Event.
// Returns (nil, nil) if the update does not represent a supported domain event.
func (n *Normalizer) Normalize(ctx context.Context, e tg.Entities, update tg.UpdateClass) (core.Event, error) {
	now := time.Now().UTC()

	switch u := update.(type) {
	case *tg.UpdateNewMessage:
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil, nil
		}
		coreMsg := extractCoreMessage(msg)
		chatID := extractChatIDFromPeer(msg.PeerID)
		return &core.MessageCreatedEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("msg:%d:%d", chatID, msg.ID)},
			At:       now,
			Message:  coreMsg,
			ChatID:   chatID,
			PeerID:   msg.PeerID,
		}, nil

	case *tg.UpdateNewChannelMessage:
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil, nil
		}
		coreMsg := extractCoreMessage(msg)
		chatID := extractChatIDFromPeer(msg.PeerID)
		return &core.MessageCreatedEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("channel_msg:%d:%d", chatID, msg.ID)},
			At:       now,
			Message:  coreMsg,
			ChatID:   chatID,
			PeerID:   msg.PeerID,
		}, nil

	case *tg.UpdateEditMessage:
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil, nil
		}
		chatID := extractChatIDFromPeer(msg.PeerID)
		return &core.MessageEditedEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("edit:%d:%d", chatID, msg.ID)},
			At:       now,
			MsgID:    msg.ID,
			ChatID:   chatID,
			Text:     msg.Message,
		}, nil

	case *tg.UpdateEditChannelMessage:
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil, nil
		}
		chatID := extractChatIDFromPeer(msg.PeerID)
		return &core.MessageEditedEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("channel_edit:%d:%d", chatID, msg.ID)},
			At:       now,
			MsgID:    msg.ID,
			ChatID:   chatID,
			Text:     msg.Message,
		}, nil

	case *tg.UpdateDeleteMessages:
		return &core.MessagesDeletedEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("del:%d", now.UnixNano())},
			At:       now,
			MsgIDs:   u.Messages,
		}, nil

	case *tg.UpdateDeleteChannelMessages:
		return &core.MessagesDeletedEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("channel_del:%d:%d", u.ChannelID, now.UnixNano())},
			At:       now,
			ChatID:   u.ChannelID,
			MsgIDs:   u.Messages,
		}, nil

	case *tg.UpdateBotCallbackQuery:
		target := core.CallbackTarget{
			Origin:       core.CallbackOriginMessage,
			Peer:         normalizeInputPeer(u.Peer, e),
			MessageID:    u.MsgID,
			ChatInstance: u.ChatInstance,
		}
		return &core.CallbackQueryEvent{
			MetaData:     core.EventMeta{ID: fmt.Sprintf("cb:%d", u.QueryID)},
			At:           now,
			Data:         u.Data,
			QueryID:      u.QueryID,
			UserID:       u.UserID,
			ChatInstance: u.ChatInstance,
			Origin:       core.CallbackOriginMessage,
			Target:       target,
		}, nil

	case *tg.UpdateInlineBotCallbackQuery:
		target := core.CallbackTarget{
			Origin:       core.CallbackOriginInline,
			InlineID:     u.MsgID,
			ChatInstance: u.ChatInstance,
		}
		return &core.CallbackQueryEvent{
			MetaData:     core.EventMeta{ID: fmt.Sprintf("inline_cb:%d", u.QueryID)},
			At:           now,
			Data:         u.Data,
			QueryID:      u.QueryID,
			UserID:       u.UserID,
			ChatInstance: u.ChatInstance,
			Origin:       core.CallbackOriginInline,
			Target:       target,
		}, nil

	case *tg.UpdateBotInlineSend:
		return &core.InlineResultChosenEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("inline_send:%s", u.ID)},
			At:       now,
			UserID:   u.UserID,
			Query:    u.Query,
			ResultID: u.ID,
			InlineID: u.MsgID,
		}, nil
	}

	return nil, nil
}

func normalizeInputPeer(peer tg.PeerClass, e tg.Entities) tg.InputPeerClass {
	if peer == nil {
		return nil
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		var accessHash int64
		if u, ok := e.Users[p.UserID]; ok && u != nil {
			accessHash = u.AccessHash
		}
		if accessHash == 0 {
			return nil
		}
		return &tg.InputPeerUser{UserID: p.UserID, AccessHash: accessHash}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		var accessHash int64
		if ch, ok := e.Channels[p.ChannelID]; ok && ch != nil {
			accessHash = ch.AccessHash
		}
		if accessHash == 0 {
			return nil
		}
		return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: accessHash}
	default:
		return nil
	}
}
