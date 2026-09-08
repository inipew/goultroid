package telegram

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func (d *Dispatcher) dispatch(ctx context.Context, e tg.Entities, msg *tg.Message) error {
	// Admission and WaitGroup.Add are serialized with Stop through d.mu. This is
	// required by sync.WaitGroup: a positive Add that starts from zero must happen
	// before Wait begins.
	d.mu.Lock()
	if !d.acceptingUpdates.Load() {
		d.mu.Unlock()
		return nil
	}
	d.inFlight.Add(1)
	d.mu.Unlock()
	defer d.inFlight.Done()

	parsed, isCmd, err := d.router.Parse(msg.Message)
	if err != nil {
		d.logger.Warn("command parse syntax error", zap.Error(err), zap.String("text", msg.Message))
		return nil
	}
	cmdName := ""
	if isCmd {
		cmdName = parsed.Name
	}

	origin := core.ExecutionInteractive
	if msg.Out {
		svc := d.getService()
		if svc != nil && svc.IsBotSent(msg.ID) {
			origin = core.ExecutionAutomation
		}
	}
	decision := core.NewMessageDecision(origin)
	ctx = core.WithMessageDecision(ctx, decision)

	if len(e.Users) > 0 || len(e.Channels) > 0 || len(e.Chats) > 0 {
		resolver := d.getResolver()
		if r, ok := resolver.(*Resolver); ok && r.storage != nil {
			job := peerUpdateJob{}
			for _, u := range e.Users {
				job.users = append(job.users, u)
			}
			for _, ch := range e.Channels {
				job.channels = append(job.channels, ch)
			}
			for _, c := range e.Chats {
				job.chats = append(job.chats, c)
			}
			if !d.stopping.Load() {
				d.mu.RLock()
				if !d.stopping.Load() && d.peerQueue != nil {
					select {
					case d.peerQueue <- job:
						d.peerEnqueued.Add(1)
					default:
						d.peerDropped.Add(1)
					}
					d.mu.RUnlock()
				} else {
					d.mu.RUnlock()
					if !d.stopping.Load() {
						go func() {
							bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							defer cancel()
							_ = r.storage.SaveEntitiesBatch(bgCtx, job.users, job.channels, job.chats)
						}()
					}
				}
			}
		}
	}

	d.mu.RLock()
	var syncHandlers []MessageHandler
	var asyncHandlers []MessageHandler
	for _, ph := range d.messageHandlers {
		if ph.priority >= PriorityObservability {
			asyncHandlers = append(asyncHandlers, ph.handler)
		} else {
			syncHandlers = append(syncHandlers, ph.handler)
		}
	}
	d.mu.RUnlock()

	for _, h := range syncHandlers {
		if d.safeExecuteInterceptor(ctx, h, e, msg, isCmd, cmdName) {
			return nil
		}
	}
	if decision.IsHandled() || decision.IsSuppressedCommands() {
		return nil
	}

	coreMsg := extractCoreMessage(msg)
	if coreMsg.GroupedID != 0 && d.albumBuffer != nil {
		d.albumBuffer.Add(coreMsg)
	}
	if !isCmd {
		for _, h := range asyncHandlers {
			_ = d.safeExecuteInterceptor(ctx, h, e, msg, isCmd, cmdName)
		}
		return nil
	}

	cmd, exists := d.router.Find(parsed.Name)
	if !exists {
		for _, h := range asyncHandlers {
			_ = d.safeExecuteInterceptor(ctx, h, e, msg, isCmd, cmdName)
		}
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

	select {
	case d.cmdSem <- struct{}{}:
	case <-execCtx.Done():
		cancel()
		return nil
	}
	d.cmdWG.Add(1)
	d.runningCommands.Add(1)
	d.totalCommands.Add(1)
	go func() {
		defer func() {
			d.runningCommands.Add(-1)
			<-d.cmdSem
			d.cmdWG.Done()
			cancel()
		}()
		_ = d.executor.Execute(coreCtx, cmd)
	}()

	for _, h := range asyncHandlers {
		_ = d.safeExecuteInterceptor(ctx, h, e, msg, isCmd, cmdName)
	}
	return nil
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
