package telegram

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

func (d *Dispatcher) dispatch(ctx context.Context, e tg.Entities, msg *tg.Message) error {
	release, accepted := d.admitIngress()
	if !accepted {
		return nil
	}
	defer release()

	// Telegram's updates manager already provides ordering/gap recovery, but the
	// same update can still be delivered more than once around reconnects. Keep
	// this transport-level guard memory-only and bounded: ordinary chat traffic
	// must never require a durable database claim just to enter the dispatcher.
	if d.ingressDedupe != nil && !d.ingressDedupe.Accept(msg, time.Now()) {
		return nil
	}

	chatID := extractChatIDFromPeer(msg.PeerID)
	parsed, isCmd, err := d.router.Parse(msg.Message)
	if err != nil {
		d.logger.Warn("command parse syntax error", zap.Error(err), zap.String("text", msg.Message))
		return nil
	}
	cmdName := ""
	if isCmd {
		cmdName = parsed.Name
	}

	var cmd core.Command
	var cmdExists bool
	if isCmd {
		cmd, cmdExists = d.router.Find(parsed.Name)
	}

	origin := core.ExecutionInteractive
	if msg.Out {
		svc := d.getService()
		if svc != nil {
			type peerAwareBotSent interface {
				IsBotSentForPeer(peer tg.PeerClass, msgID int, selfID int64) bool
			}
			if tracker, ok := svc.(peerAwareBotSent); ok {
				if tracker.IsBotSentForPeer(msg.PeerID, msg.ID, d.getSelfID()) {
					origin = core.ExecutionAutomation
				}
			} else if svc.IsBotSent(msg.ID) {
				origin = core.ExecutionAutomation
			}
		}
	}
	decision := core.NewMessageDecision(origin)
	ctx = core.WithMessageDecision(ctx, decision)

	if len(e.Users) > 0 || len(e.Channels) > 0 || len(e.Chats) > 0 {
		resolver := d.getResolver()
		if r, ok := resolver.(*Resolver); ok && r.storage != nil {
			d.enqueuePeerEntities(e)
		}
	}

	messageEnvelope := NormalizeMessageEnvelope(e, msg, isCmd, cmdName, d.getSelfID())
	decisionHandlers, eventHandlers := d.messageHandlersForEnvelope(messageEnvelope)

	if d.executeDecisionHandlersEnvelope(ctx, decisionHandlers, messageEnvelope, e, msg) {
		return nil
	}
	if decision.IsHandled() || decision.IsSuppressedCommands() {
		return nil
	}

	// Invocation is a cheap security admission gate and must run before durable
	// idempotency, peer resolution, or TaskEngine submission. Permission remains
	// a separate authorization check inside CommandExecutor.
	if cmdExists {
		senderID := invocationSenderID(msg, d.getSelfID())
		if !cmd.CanInvoke(core.ExecutionInteractive, senderID, msg.Out, d.perms) {
			d.logger.Debug("dispatcher: command invocation denied",
				zap.String("command", cmdName),
				zap.Int64("sender_id", senderID),
				zap.String("policy", cmd.EffectiveInvocation(core.ExecutionInteractive).String()),
			)
			return nil
		}
	}

	// Durability belongs to execution candidates, not transport ingress. Only a
	// recognized command that survived the synchronous decision pipeline may
	// claim durable idempotency. This keeps ordinary traffic, unknown commands,
	// and suppressed commands off the SQLite write path while preserving the
	// fail-closed guarantee before command/event side effects.
	if cmdExists && d.idempotencyMgr != nil {
		key := fmt.Sprintf("msg:%d:%d", chatID, msg.ID)
		isNew, claimErr := d.idempotencyMgr.CheckAndSet(ctx, key, 5*time.Minute)
		if claimErr != nil {
			d.logger.Error("dispatcher: command idempotency claim failed",
				zap.String("key", key),
				zap.String("command", cmdName),
				zap.Error(claimErr),
			)
			return nil
		}
		if !isNew {
			d.logger.Debug("dispatcher: duplicate command dropped by idempotency manager", zap.String("key", key))
			return nil
		}
	}

	bus := d.getEventBus()
	publishMessageEvent := bus != nil &&
		bus.HasSubscribersAtPriority(core.EventTypeMessageCreated, core.PriorityNormal)

	// core.Message performs media extraction and is materially heavier than the
	// ingress envelope. Build it only when a command, album aggregation, or an
	// actual EventBus subscriber needs it.
	needsCoreMessage := cmdExists || msg.GroupedID != 0 || publishMessageEvent
	var coreMsg *core.Message
	if needsCoreMessage {
		coreMsg = extractCoreMessage(msg)
	}
	if coreMsg != nil && coreMsg.GroupedID != 0 && d.albumBuffer != nil {
		d.albumBuffer.Add(coreMsg)
	}
	if publishMessageEvent {
		if evt := canonicalMessageCreatedEvent(msg, coreMsg, time.Now()); evt != nil {
			bus.Publish(evt)
		}
	}

	if !isCmd {
		d.dispatchEventHandlersEnvelope(ctx, eventHandlers, messageEnvelope, e, msg)
		return nil
	}

	if !cmdExists {
		d.dispatchEventHandlersEnvelope(ctx, eventHandlers, messageEnvelope, e, msg)
		return nil
	}
	if coreMsg == nil {
		// Defensive fallback: recognized commands are included in
		// needsCoreMessage above, so this should be unreachable.
		coreMsg = extractCoreMessage(msg)
	}

	peerInput := d.resolveDispatchPeer(ctx, e, msg)
	chat := d.resolveDispatchChat(e, msg)
	d.logger.Debug("dispatch: peer resolved",
		zap.String("peerType", fmt.Sprintf("%T", msg.PeerID)),
		zap.String("chatType", chat.Type),
		zap.Int64("chatID", chat.ID),
		zap.Bool("msgOut", msg.Out),
		zap.String("command", cmdName),
	)
	if peerInput == nil && msg.Out {
		peerInput = &tg.InputPeerSelf{}
	}
	if peerInput == nil && msg.PeerID != nil {
		d.logger.Warn("dispatch: peer unresolvable without access hash, command execution skipped",
			zap.Int64("chatID", chat.ID),
			zap.String("command", cmdName),
		)
		return nil
	}

	sender := &core.User{}
	if msg.Out {
		sender.ID = d.getSelfID()
	} else if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			sender.ID = u.UserID
			if ue, ok := e.Users[u.UserID]; ok {
				sender.FirstName = ue.FirstName
				sender.LastName = ue.LastName
				sender.Username = ue.Username
				sender.IsBot = ue.Bot
			}
		}
	}
	coreMsg.SenderID = sender.ID

	root := d.getRootContext()
	if root == nil {
		d.logger.Warn("dispatcher: root context is nil, falling back to background - lifecycle not correctly wired")
		root = context.Background()
	}
	execCtx, cancel := context.WithCancel(root)
	var album []*core.Message
	if coreMsg.GroupedID != 0 && d.albumBuffer != nil {
		album = d.albumBuffer.Get(coreMsg.GroupedID)
	}
	coreCtx := &core.Context{
		Ctx:       execCtx,
		Command:   parsed.Name,
		Args:      parsed.Args,
		RawArgs:   parsed.RawArgs,
		Message:   coreMsg,
		Album:     album,
		Chat:      chat,
		Sender:    sender,
		Perms:     d.perms,
		Svc:       d.getService(),
		PeerID:    peerInput,
		Resolver:  d.getResolver(),
		Localizer: d.getLocalizer(),
		EventBus:  d.getEventBus(),
	}

	taskOwner := fmt.Sprintf("telegram:user:%d", sender.ID)
	if sender.ID == 0 {
		taskOwner = "telegram:unknown"
	}
	taskID := fmt.Sprintf("cmd:%d:%d", chat.ID, msg.ID)
	correlationID := fmt.Sprintf("msg:%d:%d", chat.ID, msg.ID)
	if err := d.submitInteractiveCommand(execCtx, cancel, coreCtx, cmd, taskID, taskOwner, correlationID); err != nil {
		d.logger.Warn("interactive command admission rejected",
			zap.String("command", cmdName),
			zap.Int64("chat_id", chat.ID),
			zap.Error(err),
		)
	}

	d.dispatchEventHandlersEnvelope(ctx, eventHandlers, messageEnvelope, e, msg)
	return nil
}

// executeDecisionHandlers preserves the synchronous security/moderation
// decision contract while routing plugin code through TaskEngine. One shared
// deadline bounds total update-loop latency regardless of handler count.
func (d *Dispatcher) executeDecisionHandlers(ctx context.Context, handlers []prioritizedHandler, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) bool {
	message := NormalizeMessageEnvelope(e, msg, isCmd, cmdName, d.getSelfID())
	return d.executeDecisionHandlersEnvelope(ctx, handlers, message, e, msg)
}

func (d *Dispatcher) executeDecisionHandlersEnvelope(ctx context.Context, handlers []prioritizedHandler, message *core.MessageEnvelope, e tg.Entities, msg *tg.Message) bool {
	if message == nil || msg == nil {
		return false
	}
	if len(handlers) == 0 {
		return false
	}
	chatID := message.ChatID
	decisionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, registered := range handlers {
		if !d.messageHookStateInterested(registered, chatID) {
			continue
		}
		if registered.scope.IsZero() { // compatibility for local/test handlers
			if d.safeExecuteRegisteredInterceptor(decisionCtx, registered, e, msg, message) {
				return true
			}
			continue
		}
		client := d.taskClient()
		if client == nil {
			d.logger.Warn("decision handler execution unavailable", zap.Error(ErrTasksNotConfigured))
			return true // fail closed for security/moderation handlers
		}
		var handled atomic.Bool
		d.inFlight.Add(1)
		ticket, err := client.Submit(decisionCtx, tasks.WorkSpec{
			ID:               tasks.TaskID(fmt.Sprintf("decision:%d:%d:%d", registered.id, chatID, msg.ID)),
			Scope:            registered.scope,
			QuotaOwner:       tasks.OwnerID(registered.scope.Owner),
			Pool:             "interactive",
			Class:            tasks.PriorityInteractive,
			OrderingKey:      fmt.Sprintf("chat:%d", chatID),
			ExecutionTimeout: 5 * time.Second,
			Handler: func(taskCtx context.Context) error {
				handled.Store(d.safeExecuteRegisteredInterceptor(taskCtx, registered, e, msg, message))
				return nil
			},
			OnComplete: func(tasks.TaskResult) { d.inFlight.Done() },
		})
		if err != nil {
			d.inFlight.Done()
			d.logger.Warn("decision handler admission rejected", zap.Uint64("handler_id", registered.id), zap.Error(err))
			return true
		}
		if _, err := ticket.Wait(decisionCtx); err != nil {
			d.logger.Warn("decision handler deadline exceeded", zap.Uint64("handler_id", registered.id), zap.Error(err))
			return true
		}
		if handled.Load() {
			return true
		}
	}
	return false
}

func (d *Dispatcher) messageHookStateInterested(registered prioritizedHandler, chatID int64) (interested bool) {
	if registered.stateGate == nil {
		return true
	}
	defer func() {
		if r := recover(); r != nil {
			d.logger.Warn("message hook state gate panicked; failing open",
				zap.Uint64("handler_id", registered.id),
				zap.Any("panic", r),
			)
			interested = true
		}
	}()
	return registered.stateGate(chatID)
}

func (d *Dispatcher) dispatchEventHandlersEnvelope(ctx context.Context, handlers []prioritizedHandler, message *core.MessageEnvelope, e tg.Entities, msg *tg.Message) {
	if len(handlers) == 0 || message == nil || msg == nil {
		return
	}
	client := d.taskClient()
	if client == nil {
		d.logger.Warn("observer execution unavailable", zap.Error(ErrTasksNotConfigured))
		return
	}
	chatID := message.ChatID
	for _, registered := range handlers {
		registered := registered
		if !d.messageHookStateInterested(registered, chatID) {
			continue
		}
		d.inFlight.Add(1)
		owner := tasks.OwnerID("telegram:feature")
		class := tasks.PriorityNormal
		if registered.priority >= PriorityObservability {
			owner = "telegram:observability"
			class = tasks.PriorityBackground
		}
		if !registered.scope.IsZero() {
			owner = tasks.OwnerID(registered.scope.Owner)
		}
		_, err := client.Submit(ctx, tasks.WorkSpec{
			ID:               tasks.TaskID(fmt.Sprintf("hook:%d:%d:%d", registered.id, chatID, msg.ID)),
			Scope:            registered.scope,
			QuotaOwner:       owner,
			Pool:             "general",
			Class:            class,
			OrderingKey:      fmt.Sprintf("chat:%d", chatID),
			ExecutionTimeout: 10 * time.Second,
			Handler: func(taskCtx context.Context) error {
				_ = d.safeExecuteRegisteredInterceptor(taskCtx, registered, e, msg, message)
				return nil
			},
			OnComplete: func(tasks.TaskResult) { d.inFlight.Done() },
		})
		if err != nil {
			d.inFlight.Done()
			d.logger.Debug("message hook admission rejected", zap.Uint64("handler_id", registered.id), zap.Error(err))
		}
	}
}

func (d *Dispatcher) resolveDispatchChat(e tg.Entities, msg *tg.Message) *core.Chat {
	chat := &core.Chat{}
	switch p := msg.PeerID.(type) {
	case *tg.PeerUser:
		chat.ID = p.UserID
		chat.Type = "private"
		if u, ok := e.Users[p.UserID]; ok {
			chat.Username = u.Username
			chat.Title = u.FirstName + " " + u.LastName
			chat.AccessHash = u.AccessHash
		}
	case *tg.PeerChat:
		chat.ID = p.ChatID
		chat.Type = "group"
		if c, ok := e.Chats[p.ChatID]; ok {
			chat.Title = c.Title
		}
	case *tg.PeerChannel:
		chat.ID = p.ChannelID
		chat.Type = "supergroup"
		if ch, ok := e.Channels[p.ChannelID]; ok {
			chat.Title = ch.Title
			chat.Username = ch.Username
			if ch.Megagroup {
				chat.Type = "supergroup"
			} else {
				chat.Type = "channel"
			}
			chat.AccessHash = ch.AccessHash
		}
	}
	return chat
}

func (d *Dispatcher) resolveDispatchPeer(ctx context.Context, e tg.Entities, msg *tg.Message) tg.InputPeerClass {
	r := d.getResolver()
	switch p := msg.PeerID.(type) {
	case *tg.PeerUser:
		if p.UserID == d.getSelfID() {
			return &tg.InputPeerSelf{}
		}
		if u, ok := e.Users[p.UserID]; ok && u.AccessHash != 0 {
			return &tg.InputPeerUser{UserID: p.UserID, AccessHash: u.AccessHash}
		}
		if r != nil {
			if peer, _, err := r.ResolveUser(ctx, strconv.FormatInt(p.UserID, 10)); err == nil {
				if ip, ok := peer.(*tg.InputPeerUser); ok && ip.AccessHash != 0 {
					return ip
				}
			}
		}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}
	case *tg.PeerChannel:
		if ch, ok := e.Channels[p.ChannelID]; ok && ch.AccessHash != 0 {
			return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: ch.AccessHash}
		}
		if r != nil {
			if peer, err := r.ResolveChat(ctx, fmt.Sprintf("-100%d", p.ChannelID)); err == nil {
				if ip, ok := peer.(*tg.InputPeerChannel); ok && ip.AccessHash != 0 {
					return ip
				}
			}
		}
	}
	return nil
}

func extractCoreMessage(msg *tg.Message) *core.Message {
	coreMsg := &core.Message{
		ID:         msg.ID,
		Text:       msg.Message,
		Date:       time.Unix(int64(msg.Date), 0),
		IsOutgoing: msg.Out,
		GroupedID:  msg.GroupedID,
		Entities:   msg.Entities,
	}
	if msg.Media != nil {
		coreMsg.Media = core.ExtractMediaFromTG(msg.Media)
		if coreMsg.Media != nil {
			coreMsg.MediaType = coreMsg.Media.Type
		}
	}
	if msg.ReplyTo != nil {
		if header, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok {
			coreMsg.ReplyToID = header.ReplyToMsgID
			if header.ForumTopic || header.ReplyToTopID != 0 {
				if header.ReplyToTopID != 0 {
					coreMsg.TopicID = header.ReplyToTopID
				} else {
					coreMsg.TopicID = header.ReplyToMsgID
				}
			}
		}
	}
	return coreMsg
}

func invocationSenderID(msg *tg.Message, selfID int64) int64 {
	if msg == nil {
		return 0
	}
	if msg.Out {
		return selfID
	}
	if from, ok := msg.FromID.(*tg.PeerUser); ok && from != nil {
		return from.UserID
	}
	if peer, ok := msg.PeerID.(*tg.PeerUser); ok && peer != nil {
		return peer.UserID
	}
	return 0
}
