package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// RegisterHooks binds message, edit, delete, callback query, inline query, and reaction handlers to a tg.UpdateDispatcher.
func (d *Dispatcher) RegisterHooks(dispatcher *tg.UpdateDispatcher) {
	dispatcher.OnNewMessage(d.OnNewMessage)
	dispatcher.OnNewChannelMessage(d.OnNewChannelMessage)
	dispatcher.OnEditMessage(d.OnEditMessage)
	dispatcher.OnEditChannelMessage(d.OnEditChannelMessage)
	dispatcher.OnDeleteMessages(d.OnDeleteMessages)
	dispatcher.OnDeleteChannelMessages(d.OnDeleteChannelMessages)
	dispatcher.OnBotCallbackQuery(d.OnBotCallbackQuery)
	dispatcher.OnInlineBotCallbackQuery(d.OnInlineBotCallbackQuery)
	dispatcher.OnBotInlineQuery(d.OnBotInlineQuery)
	dispatcher.OnBotInlineSend(d.OnBotInlineSend)
	dispatcher.OnMessageReactions(d.OnMessageReactions)
}

// OnNewMessage handles private and standard group message updates.
func (d *Dispatcher) OnNewMessage(ctx context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
	msg, ok := update.Message.(*tg.Message)
	if !ok {
		return nil
	}
	return d.dispatch(ctx, e, msg)
}

// OnNewChannelMessage handles supergroup and channel message updates.
func (d *Dispatcher) OnNewChannelMessage(ctx context.Context, e tg.Entities, update *tg.UpdateNewChannelMessage) error {
	msg, ok := update.Message.(*tg.Message)
	if !ok {
		return nil
	}
	return d.dispatch(ctx, e, msg)
}

// OnEditMessage handles edits in private chats and standard groups.
func (d *Dispatcher) OnEditMessage(ctx context.Context, e tg.Entities, update *tg.UpdateEditMessage) error {
	if !d.acceptingUpdates.Load() {
		return nil
	}
	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	msg, ok := update.Message.(*tg.Message)
	if !ok {
		return nil
	}
	chatID := extractChatIDFromPeer(msg.PeerID)
	bus.Publish(&core.MessageEditedEvent{
		At:     time.Now(),
		MsgID:  msg.ID,
		ChatID: chatID,
		Text:   msg.Message,
	})
	return nil
}

// OnEditChannelMessage handles edits in supergroups and channels.
func (d *Dispatcher) OnEditChannelMessage(ctx context.Context, e tg.Entities, update *tg.UpdateEditChannelMessage) error {
	if !d.acceptingUpdates.Load() {
		return nil
	}
	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	msg, ok := update.Message.(*tg.Message)
	if !ok {
		return nil
	}
	chatID := extractChatIDFromPeer(msg.PeerID)
	bus.Publish(&core.MessageEditedEvent{
		At:     time.Now(),
		MsgID:  msg.ID,
		ChatID: chatID,
		Text:   msg.Message,
	})
	return nil
}

// OnDeleteMessages handles bulk message deletions in private chats and standard groups.
func (d *Dispatcher) OnDeleteMessages(ctx context.Context, e tg.Entities, update *tg.UpdateDeleteMessages) error {
	if !d.acceptingUpdates.Load() {
		return nil
	}
	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	bus.Publish(&core.MessagesDeletedEvent{
		At:          time.Now(),
		ChatID:      0,
		PeerUnknown: true,
		MsgIDs:      update.Messages,
	})
	return nil
}

// OnDeleteChannelMessages handles bulk message deletions in supergroups and channels.
func (d *Dispatcher) OnDeleteChannelMessages(ctx context.Context, e tg.Entities, update *tg.UpdateDeleteChannelMessages) error {
	if !d.acceptingUpdates.Load() {
		return nil
	}
	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	bus.Publish(&core.MessagesDeletedEvent{
		At:     time.Now(),
		ChatID: update.ChannelID,
		MsgIDs: update.Messages,
	})
	return nil
}

// callbackInputPeer converts a Telegram PeerClass to InputPeerClass using Entities + resolver fallback.
func (d *Dispatcher) callbackInputPeer(ctx context.Context, peer tg.PeerClass, e tg.Entities) tg.InputPeerClass {
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
			if resolver := d.getResolver(); resolver != nil {
				if resolved, _, err := resolver.ResolveUser(ctx, fmt.Sprintf("%d", p.UserID)); err == nil {
					if ipu, ok := resolved.(*tg.InputPeerUser); ok {
						accessHash = ipu.AccessHash
					}
				}
			}
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
			if resolver := d.getResolver(); resolver != nil {
				if resolved, err := resolver.ResolveChat(ctx, fmt.Sprintf("-100%d", p.ChannelID)); err == nil {
					if ipc, ok := resolved.(*tg.InputPeerChannel); ok {
						accessHash = ipc.AccessHash
					}
				}
			}
		}
		if accessHash == 0 {
			return nil
		}
		return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: accessHash}
	default:
		return nil
	}
}

// OnBotCallbackQuery handles inline keyboard button callback queries.
func (d *Dispatcher) OnBotCallbackQuery(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
	if !d.acceptingUpdates.Load() {
		return nil
	}
	chatID := extractChatIDFromPeer(update.Peer)
	inputPeer := d.callbackInputPeer(ctx, update.Peer, e)
	target := core.CallbackTarget{
		Origin:       core.CallbackOriginMessage,
		Peer:         inputPeer,
		MessageID:    update.MsgID,
		ChatInstance: update.ChatInstance,
	}
	evt := &core.CallbackQueryEvent{
		At:           time.Now(),
		QueryID:      update.QueryID,
		UserID:       update.UserID,
		ChatID:       chatID,
		MsgID:        update.MsgID,
		Data:         update.Data,
		Origin:       core.CallbackOriginMessage,
		Target:       target,
		ChatInstance: update.ChatInstance,
	}

	bus := d.getEventBus()
	if bus != nil {
		bus.Publish(evt)
	}

	cbRouter := d.getCallbackRouter()
	if cbRouter != nil {
		_ = cbRouter.Dispatch(ctx, evt, d.getService())
	}
	return nil
}

// OnInlineBotCallbackQuery handles inline message button callback queries.
func (d *Dispatcher) OnInlineBotCallbackQuery(ctx context.Context, e tg.Entities, update *tg.UpdateInlineBotCallbackQuery) error {
	if !d.acceptingUpdates.Load() {
		return nil
	}
	target := core.CallbackTarget{
		Origin:       core.CallbackOriginInline,
		InlineID:     update.MsgID,
		ChatInstance: update.ChatInstance,
	}
	evt := &core.CallbackQueryEvent{
		At:           time.Now(),
		QueryID:      update.QueryID,
		UserID:       update.UserID,
		ChatID:       0,
		MsgID:        0,
		Data:         update.Data,
		Origin:       core.CallbackOriginInline,
		Target:       target,
		ChatInstance: update.ChatInstance,
	}

	bus := d.getEventBus()
	if bus != nil {
		bus.Publish(evt)
	}

	cbRouter := d.getCallbackRouter()
	if cbRouter != nil {
		_ = cbRouter.Dispatch(ctx, evt, d.getService())
	}
	return nil
}

// OnBotInlineQuery handles incoming inline search query requests.
func (d *Dispatcher) OnBotInlineQuery(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineQuery) error {
	if !d.acceptingUpdates.Load() {
		return nil
	}
	engine := d.getInlineEngine()
	if engine == nil {
		return nil
	}
	return engine.ExecuteWithPeerType(ctx, d.getService(), update.QueryID, update.UserID, update.Query, update.Offset, update.PeerType)
}

// OnBotInlineSend handles inline result chosen feedback (observational only).
func (d *Dispatcher) OnBotInlineSend(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineSend) error {
	if !d.acceptingUpdates.Load() {
		return nil
	}
	bus := d.getEventBus()
	if bus != nil {
		var inlineID tg.InputBotInlineMessageIDClass
		if msgID, ok := update.GetMsgID(); ok {
			inlineID = msgID
		} else if update.MsgID != nil {
			inlineID = update.MsgID
		}
		bus.Publish(&core.InlineResultChosenEvent{
			At:       time.Now(),
			UserID:   update.UserID,
			Query:    update.Query,
			ResultID: update.ID,
			InlineID: inlineID,
		})
	}
	return nil
}

// OnMessageReactions handles reaction updates on messages.
func (d *Dispatcher) OnMessageReactions(ctx context.Context, e tg.Entities, update *tg.UpdateMessageReactions) error {
	if !d.acceptingUpdates.Load() {
		return nil
	}
	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	chatID := extractChatIDFromPeer(update.Peer)
	bus.Publish(&core.ReactionUpdatedEvent{
		At:     time.Now(),
		MsgID:  update.MsgID,
		ChatID: chatID,
	})
	return nil
}

// extractChatIDFromPeer returns a numeric chat ID for the given peer class.
func extractChatIDFromPeer(peer tg.PeerClass) int64 {
	if peer == nil {
		return 0
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChat:
		return p.ChatID
	case *tg.PeerChannel:
		return p.ChannelID
	}
	return 0
}
