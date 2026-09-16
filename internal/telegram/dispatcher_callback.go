package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
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
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	if d.normalizer != nil {
		evt, err := d.normalizer.Normalize(ctx, e, update)
		if err == nil && evt != nil {
			bus.Publish(evt)
		}
	}
	return nil
}

// OnEditChannelMessage handles edits in supergroups and channels.
func (d *Dispatcher) OnEditChannelMessage(ctx context.Context, e tg.Entities, update *tg.UpdateEditChannelMessage) error {
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	if d.normalizer != nil {
		evt, err := d.normalizer.Normalize(ctx, e, update)
		if err == nil && evt != nil {
			bus.Publish(evt)
		}
	}
	return nil
}

// OnDeleteMessages handles bulk message deletions in private chats and standard groups.
func (d *Dispatcher) OnDeleteMessages(ctx context.Context, e tg.Entities, update *tg.UpdateDeleteMessages) error {
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	if d.normalizer != nil {
		evt, err := d.normalizer.Normalize(ctx, e, update)
		if err == nil && evt != nil {
			bus.Publish(evt)
		}
	}
	return nil
}

// OnDeleteChannelMessages handles bulk message deletions in supergroups and channels.
func (d *Dispatcher) OnDeleteChannelMessages(ctx context.Context, e tg.Entities, update *tg.UpdateDeleteChannelMessages) error {
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	if d.normalizer != nil {
		evt, err := d.normalizer.Normalize(ctx, e, update)
		if err == nil && evt != nil {
			bus.Publish(evt)
		}
	}
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
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	if d.idempotencyMgr != nil {
		key := fmt.Sprintf("cb:%d", update.QueryID)
		isNew, err := d.idempotencyMgr.CheckAndSet(ctx, key, 5*time.Minute)
		if err == nil && !isNew {
			return nil
		}
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
	if cbRouter == nil {
		if svc := d.getService(); svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Interaction service unavailable.", false)
		}
		return nil
	}

	scope, available := cbRouter.TaskScope(evt.Data, d.resolvePluginScope)
	if !available {
		if svc := d.getService(); svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Feature not available.", false)
		}
		return nil
	}
	client := d.taskClient()
	if client != nil {
		taskID := fmt.Sprintf("cb:%d", evt.QueryID)
		owner := fmt.Sprintf("telegram:user:%d", evt.UserID)
		d.inFlight.Add(1)
		_, err := client.Submit(ctx, tasks.WorkSpec{
			ID:               tasks.TaskID(taskID),
			Scope:            scope,
			QuotaOwner:       tasks.OwnerID(owner),
			Pool:             "interactive",
			Class:            tasks.PriorityInteractive,
			OrderingKey:      callbackOrderingKey(evt),
			ExecutionTimeout: 15 * time.Second,
			Handler: func(taskCtx context.Context) error {
				return cbRouter.Dispatch(taskCtx, evt, d.getService())
			},
			OnComplete: func(tasks.TaskResult) { d.inFlight.Done() },
		})
		if err != nil {
			d.inFlight.Done()
			d.logger.Warn("callback dispatch admission rejected", zap.Int64("query_id", evt.QueryID), zap.Error(err))
			if svc := d.getService(); svc != nil {
				_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Server is overloaded, please try again shortly.", false)
			}
		}
	} else {
		d.logger.Warn("callback execution unavailable", zap.Error(ErrTasksNotConfigured))
		if svc := d.getService(); svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Service unavailable.", false)
		}
	}
	return nil
}

// OnInlineBotCallbackQuery handles inline message button callback queries.
func (d *Dispatcher) OnInlineBotCallbackQuery(ctx context.Context, e tg.Entities, update *tg.UpdateInlineBotCallbackQuery) error {
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	if d.idempotencyMgr != nil {
		key := fmt.Sprintf("inline_cb:%d", update.QueryID)
		isNew, err := d.idempotencyMgr.CheckAndSet(ctx, key, 5*time.Minute)
		if err == nil && !isNew {
			return nil
		}
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
	if cbRouter == nil {
		if svc := d.getService(); svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Interaction service unavailable.", false)
		}
		return nil
	}

	scope, available := cbRouter.TaskScope(evt.Data, d.resolvePluginScope)
	if !available {
		if svc := d.getService(); svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Feature not available.", false)
		}
		return nil
	}
	client := d.taskClient()
	if client != nil {
		taskID := fmt.Sprintf("inline_cb:%d", evt.QueryID)
		owner := fmt.Sprintf("telegram:user:%d", evt.UserID)
		d.inFlight.Add(1)
		_, err := client.Submit(ctx, tasks.WorkSpec{
			ID:               tasks.TaskID(taskID),
			Scope:            scope,
			QuotaOwner:       tasks.OwnerID(owner),
			Pool:             "interactive",
			Class:            tasks.PriorityInteractive,
			OrderingKey:      callbackOrderingKey(evt),
			ExecutionTimeout: 15 * time.Second,
			Handler: func(taskCtx context.Context) error {
				return cbRouter.Dispatch(taskCtx, evt, d.getService())
			},
			OnComplete: func(tasks.TaskResult) { d.inFlight.Done() },
		})
		if err != nil {
			d.inFlight.Done()
			d.logger.Warn("inline callback dispatch admission rejected", zap.Int64("query_id", evt.QueryID), zap.Error(err))
			if svc := d.getService(); svc != nil {
				_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Server is overloaded, please try again shortly.", false)
			}
		}
	} else {
		d.logger.Warn("inline callback execution unavailable", zap.Error(ErrTasksNotConfigured))
		if svc := d.getService(); svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Service unavailable.", false)
		}
	}
	return nil
}

// OnBotInlineQuery handles incoming inline search query requests.
func (d *Dispatcher) OnBotInlineQuery(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineQuery) error {
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	engine := d.getInlineEngine()
	if engine == nil {
		return nil
	}
	client := d.taskClient()
	if client != nil {
		taskID := fmt.Sprintf("inline:%d", update.QueryID)
		owner := fmt.Sprintf("telegram:user:%d", update.UserID)
		d.inFlight.Add(1)
		_, err := client.Submit(ctx, tasks.WorkSpec{
			ID:               tasks.TaskID(taskID),
			QuotaOwner:       tasks.OwnerID(owner),
			Pool:             "interactive",
			Class:            tasks.PriorityInteractive,
			OrderingKey:      fmt.Sprintf("inline:%d", update.QueryID),
			ExecutionTimeout: 5 * time.Second,
			Handler: func(taskCtx context.Context) error {
				return engine.ExecuteWithPeerType(taskCtx, d.getService(), update.QueryID, update.UserID, update.Query, update.Offset, update.PeerType)
			},
			OnComplete: func(tasks.TaskResult) { d.inFlight.Done() },
		})
		if err != nil {
			d.inFlight.Done()
			d.logger.Warn("inline query admission rejected", zap.Int64("query_id", update.QueryID), zap.Error(err))
			return err
		}
		return nil
	}
	return ErrTasksNotConfigured
}

// OnBotInlineSend handles inline result chosen feedback (observational only).
func (d *Dispatcher) OnBotInlineSend(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineSend) error {
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	if d.normalizer != nil {
		evt, err := d.normalizer.Normalize(ctx, e, update)
		if err == nil && evt != nil {
			bus.Publish(evt)
		}
	}
	return nil
}

// OnMessageReactions handles reaction updates on messages.
func (d *Dispatcher) OnMessageReactions(ctx context.Context, e tg.Entities, update *tg.UpdateMessageReactions) error {
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	if d.normalizer != nil {
		evt, err := d.normalizer.Normalize(ctx, e, update)
		if err == nil && evt != nil {
			bus.Publish(evt)
		}
	}
	return nil
}

func callbackOrderingKey(evt *core.CallbackQueryEvent) string {
	if evt == nil {
		return ""
	}
	if !evt.IsInline() {
		if evt.ChatID != 0 && evt.MsgID != 0 {
			return fmt.Sprintf("callback:msg:%d:%d", evt.ChatID, evt.MsgID)
		}
		if evt.MsgID != 0 {
			return fmt.Sprintf("callback:msg:%d", evt.MsgID)
		}
		return fmt.Sprintf("callback:%d", evt.QueryID)
	}
	if evt.Target.InlineID != nil {
		switch id := evt.Target.InlineID.(type) {
		case *tg.InputBotInlineMessageID:
			return fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
		case *tg.InputBotInlineMessageID64:
			return fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
		}
	}
	if evt.ChatInstance != 0 {
		return fmt.Sprintf("callback:instance:%d", evt.ChatInstance)
	}
	return fmt.Sprintf("inline_callback:%d", evt.QueryID)
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
