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

	if d.idempotencyMgr != nil {
		key := fmt.Sprintf("msg:%d:%d", chatID, msg.ID)
		isNew, claimErr := d.idempotencyMgr.CheckAndSet(ctx, key, 5*time.Minute)
		if claimErr != nil {
			d.logger.Error("dispatcher: idempotency claim failed",
				zap.String("key", key),
				zap.Bool("command", isCmd),
				zap.Error(claimErr),
			)
			// Commands may produce external side effects, so deduplication failure is
			// an admission failure. Ordinary messages remain available for
			// non-mutating hooks while the failure is surfaced operationally.
			if isCmd {
				return nil
			}
		} else if !isNew {
			d.logger.Debug("dispatcher: duplicate message dropped by idempotency manager", zap.String("key", key))
			return nil
		}
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

	d.mu.RLock()
	var syncHandlers []prioritizedHandler
	var asyncHandlers []prioritizedHandler
	for _, ph := range d.messageHandlers {
		if ph.priority >= PriorityObservability || (ph.priority >= PriorityFeature && !ph.scope.IsZero()) {
			asyncHandlers = append(asyncHandlers, ph)
		} else {
			syncHandlers = append(syncHandlers, ph)
		}
	}
	d.mu.RUnlock()

	if d.executeDecisionHandlers(ctx, syncHandlers, e, msg, isCmd, cmdName) {
		return nil
	}
	if decision.IsHandled() || decision.IsSuppressedCommands() {
		return nil
	}

	coreMsg := extractCoreMessage(msg)
	if coreMsg.GroupedID != 0 && d.albumBuffer != nil {
		d.albumBuffer.Add(coreMsg)
	}

	if bus := d.getEventBus(); bus != nil {
		bus.Publish(&core.MessageCreatedEvent{
			MetaData: core.EventMeta{ID: fmt.Sprintf("msg:%d:%d", chatID, msg.ID)},
			At:       time.Now().UTC(),
			Message:  coreMsg,
			ChatID:   chatID,
			PeerID:   msg.PeerID,
		})
	}

	if !isCmd {
		d.dispatchAsyncHandlers(ctx, asyncHandlers, e, msg, isCmd, cmdName)
		return nil
	}

	cmd, exists := d.router.Find(parsed.Name)
	if !exists {
		d.dispatchAsyncHandlers(ctx, asyncHandlers, e, msg, isCmd, cmdName)
		return nil
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

	d.dispatchAsyncHandlers(ctx, asyncHandlers, e, msg, isCmd, cmdName)
	return nil
}

// executeDecisionHandlers preserves the synchronous security/moderation
// decision contract while routing plugin code through TaskEngine. One shared
// deadline bounds total update-loop latency regardless of handler count.
func (d *Dispatcher) executeDecisionHandlers(ctx context.Context, handlers []prioritizedHandler, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) bool {
	decisionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, registered := range handlers {
		if registered.scope.IsZero() { // compatibility for local/test handlers
			if d.safeExecuteInterceptor(decisionCtx, registered.handler, e, msg, isCmd, cmdName, registered.failurePolicy) {
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
			ID:               tasks.TaskID(fmt.Sprintf("decision:%d:%d:%d", registered.id, extractChatIDFromPeer(msg.PeerID), msg.ID)),
			Scope:            registered.scope,
			QuotaOwner:       tasks.OwnerID(registered.scope.Owner),
			Pool:             "interactive",
			Class:            tasks.PriorityInteractive,
			OrderingKey:      fmt.Sprintf("chat:%d", extractChatIDFromPeer(msg.PeerID)),
			ExecutionTimeout: 5 * time.Second,
			Handler: func(taskCtx context.Context) error {
				handled.Store(d.safeExecuteInterceptor(taskCtx, registered.handler, e, msg, isCmd, cmdName, registered.failurePolicy))
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

func (d *Dispatcher) dispatchAsyncHandlers(ctx context.Context, handlers []prioritizedHandler, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) {
	if len(handlers) == 0 {
		return
	}
	client := d.taskClient()
	if client == nil {
		d.logger.Warn("observer execution unavailable", zap.Error(ErrTasksNotConfigured))
		return
	}
	for _, registered := range handlers {
		registered := registered
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
			ID:               tasks.TaskID(fmt.Sprintf("hook:%d:%d:%d", registered.id, extractChatIDFromPeer(msg.PeerID), msg.ID)),
			Scope:            registered.scope,
			QuotaOwner:       owner,
			Pool:             "general",
			Class:            class,
			OrderingKey:      fmt.Sprintf("chat:%d", extractChatIDFromPeer(msg.PeerID)),
			ExecutionTimeout: 10 * time.Second,
			Handler: func(taskCtx context.Context) error {
				_ = d.safeExecuteInterceptor(taskCtx, registered.handler, e, msg, isCmd, cmdName, registered.failurePolicy)
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
