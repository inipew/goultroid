package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// UpdateNormalizer is the narrow normalization contract used by Dispatcher.
// Keeping this as an interface lets tests prove that uninterested event types
// never pay normalization cost.
type UpdateNormalizer interface {
	Normalize(ctx context.Context, e tg.Entities, update tg.UpdateClass) (core.Event, error)
}

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
		return canonicalMessageCreatedEvent(msg, extractCoreMessage(msg), now), nil

	case *tg.UpdateNewChannelMessage:
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil, nil
		}
		return canonicalMessageCreatedEvent(msg, extractCoreMessage(msg), now), nil

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
			MetaData:    core.EventMeta{ID: fmt.Sprintf("del:%d", now.UnixNano())},
			At:          now,
			PeerUnknown: true,
			MsgIDs:      u.Messages,
		}, nil

	case *tg.UpdateDeleteChannelMessages:
		return &core.MessagesDeletedEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("channel_del:%d:%d", u.ChannelID, now.UnixNano())},
			At:       now,
			ChatID:   u.ChannelID,
			MsgIDs:   u.Messages,
		}, nil

	case *tg.UpdateMessageReactions:
		chatID := extractChatIDFromPeer(u.Peer)
		return &core.ReactionUpdatedEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("reaction:%d:%d", chatID, u.MsgID)},
			At:       now,
			MsgID:    u.MsgID,
			ChatID:   chatID,
		}, nil

	case *tg.UpdateBotCallbackQuery:
		return canonicalCallbackQueryEvent(u, normalizeInputPeer(u.Peer, e), now), nil

	case *tg.UpdateInlineBotCallbackQuery:
		return canonicalInlineCallbackQueryEvent(u, now), nil

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

func canonicalMessageCreatedEvent(msg *tg.Message, coreMsg *core.Message, now time.Time) *core.MessageCreatedEvent {
	if msg == nil {
		return nil
	}
	if coreMsg == nil {
		coreMsg = extractCoreMessage(msg)
	}
	chatID := extractChatIDFromPeer(msg.PeerID)
	return &core.MessageCreatedEvent{
		MetaData: core.EventMeta{ID: fmt.Sprintf("msg:%d:%d", chatID, msg.ID)},
		At:       now.UTC(),
		Message:  coreMsg,
		ChatID:   chatID,
		PeerID:   msg.PeerID,
	}
}

func canonicalCallbackQueryEvent(update *tg.UpdateBotCallbackQuery, inputPeer tg.InputPeerClass, now time.Time) *core.CallbackQueryEvent {
	if update == nil {
		return nil
	}
	chatID := extractChatIDFromPeer(update.Peer)
	target := core.CallbackTarget{
		Origin:       core.CallbackOriginMessage,
		Peer:         inputPeer,
		MessageID:    update.MsgID,
		ChatInstance: update.ChatInstance,
	}
	return &core.CallbackQueryEvent{
		MetaData:     core.EventMeta{ID: fmt.Sprintf("cb:%d", update.QueryID)},
		At:           now.UTC(),
		QueryID:      update.QueryID,
		UserID:       update.UserID,
		ChatID:       chatID,
		MsgID:        update.MsgID,
		Data:         update.Data,
		Origin:       core.CallbackOriginMessage,
		Target:       target,
		ChatInstance: update.ChatInstance,
	}
}

func canonicalInlineCallbackQueryEvent(update *tg.UpdateInlineBotCallbackQuery, now time.Time) *core.CallbackQueryEvent {
	if update == nil {
		return nil
	}
	target := core.CallbackTarget{
		Origin:       core.CallbackOriginInline,
		InlineID:     update.MsgID,
		ChatInstance: update.ChatInstance,
	}
	return &core.CallbackQueryEvent{
		MetaData:     core.EventMeta{ID: fmt.Sprintf("inline_cb:%d", update.QueryID)},
		At:           now.UTC(),
		QueryID:      update.QueryID,
		UserID:       update.UserID,
		Data:         update.Data,
		Origin:       core.CallbackOriginInline,
		Target:       target,
		ChatInstance: update.ChatInstance,
	}
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
