package telegram

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// Dispatcher processes incoming Telegram updates and routes them to userbot commands.
type Dispatcher struct {
	router   *core.Router
	perms    *core.Permissions
	svc      core.TelegramServicer
	logger   *zap.Logger
	cooldown *core.CooldownTracker
	executor *core.CommandExecutor
	selfID   int64
	resolver core.PeerResolver
	rootCtx  context.Context

	messageHandlers []MessageHandler
	mu              sync.RWMutex
}

// MessageHandler is invoked for each incoming message.
type MessageHandler func(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error

// NewDispatcher creates a new Dispatcher instance.
func NewDispatcher(
	router *core.Router,
	perms *core.Permissions,
	svc core.TelegramServicer,
	logger *zap.Logger,
) *Dispatcher {
	if logger == nil {
		logger = zap.NewNop()
	}
	cooldown := core.NewCooldownTracker()
	executor := core.NewCommandExecutor(logger, cooldown, 30*time.Second)
	return &Dispatcher{
		router:   router,
		perms:    perms,
		svc:      svc,
		logger:   logger,
		cooldown: cooldown,
		executor: executor,
	}
}

// SetRootContext sets the application root context used for command lifetime coordination.
func (d *Dispatcher) SetRootContext(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rootCtx = ctx
}

func (d *Dispatcher) getRootContext() context.Context {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.rootCtx
}

// AddMessageHandler registers an interceptor for raw message processing (e.g. AFK, filters).
func (d *Dispatcher) AddMessageHandler(h MessageHandler) {
	if h == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.messageHandlers = append(d.messageHandlers, h)
}

// SetService updates the TelegramServicer instance (e.g. once client is connected).
func (d *Dispatcher) SetService(svc core.TelegramServicer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.svc = svc
}

// SetResolver updates the PeerResolver instance.
func (d *Dispatcher) SetResolver(resolver core.PeerResolver) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolver = resolver
}

func (d *Dispatcher) getResolver() core.PeerResolver {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.resolver
}

// SetSelfID sets the current logged-in user ID.
func (d *Dispatcher) SetSelfID(id int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.selfID = id
}

func (d *Dispatcher) getSelfID() int64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.selfID
}

func (d *Dispatcher) getService() core.TelegramServicer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.svc
}

// Service returns the configured TelegramServicer.
func (d *Dispatcher) Service() core.TelegramServicer {
	return d.getService()
}

// RegisterHooks binds NewMessage and NewChannelMessage handlers to a tg.UpdateDispatcher.
func (d *Dispatcher) RegisterHooks(dispatcher *tg.UpdateDispatcher) {
	dispatcher.OnNewMessage(d.OnNewMessage)
	dispatcher.OnNewChannelMessage(d.OnNewChannelMessage)
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

func (d *Dispatcher) dispatch(ctx context.Context, e tg.Entities, msg *tg.Message) error {
	parsed, isCmd := d.router.Parse(msg.Message)
	cmdName := ""
	if isCmd {
		cmdName = parsed.Name
	}

	// Run message interceptors (e.g. AFK, filters).
	d.mu.RLock()
	handlers := make([]MessageHandler, len(d.messageHandlers))
	copy(handlers, d.messageHandlers)
	d.mu.RUnlock()

	for _, h := range handlers {
		d.safeExecuteInterceptor(ctx, h, e, msg, isCmd, cmdName)
	}

	if !isCmd {
		return nil
	}

	cmd, exists := d.router.Find(parsed.Name)
	if !exists {
		return nil
	}

	coreMsg := &core.Message{
		ID:         msg.ID,
		Text:       msg.Message,
		Date:       time.Unix(int64(msg.Date), 0),
		IsOutgoing: msg.Out,
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

	var peerInput tg.InputPeerClass
	chat := &core.Chat{}

	switch p := msg.PeerID.(type) {
	case *tg.PeerUser:
		chat.ID = p.UserID
		chat.Type = "private"
		var accessHash int64
		if u, ok := e.Users[p.UserID]; ok {
			chat.Username = u.Username
			chat.Title = u.FirstName + " " + u.LastName
			accessHash = u.AccessHash
		} else if p.UserID == d.getSelfID() {
			peerInput = &tg.InputPeerSelf{}
		}
		if peerInput == nil {
			if accessHash == 0 && d.getResolver() != nil {
				if resolved, _, err := d.getResolver().ResolveUser(ctx, strconv.FormatInt(p.UserID, 10)); err == nil {
					if ipu, ok := resolved.(*tg.InputPeerUser); ok && ipu.AccessHash != 0 {
						accessHash = ipu.AccessHash
					}
				}
			}
			peerInput = &tg.InputPeerUser{UserID: p.UserID, AccessHash: accessHash}
		}
	case *tg.PeerChat:
		chat.ID = p.ChatID
		chat.Type = "group"
		peerInput = &tg.InputPeerChat{ChatID: p.ChatID}
		if c, ok := e.Chats[p.ChatID]; ok {
			chat.Title = c.Title
		}
	case *tg.PeerChannel:
		chat.ID = p.ChannelID
		chat.Type = "supergroup"
		var accessHash int64
		if ch, ok := e.Channels[p.ChannelID]; ok {
			chat.Title = ch.Title
			chat.Username = ch.Username
			if ch.Megagroup {
				chat.Type = "supergroup"
			} else {
				chat.Type = "channel"
			}
			accessHash = ch.AccessHash
		}
		if accessHash == 0 && d.getResolver() != nil {
			if resolved, err := d.getResolver().ResolveChat(ctx, fmt.Sprintf("-100%d", p.ChannelID)); err == nil {
				if ipc, ok := resolved.(*tg.InputPeerChannel); ok && ipc.AccessHash != 0 {
					accessHash = ipc.AccessHash
				}
			}
		}
		peerInput = &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: accessHash}
	}

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

	sender := &core.User{}
	if msg.Out {
		sender.ID = d.getSelfID()
	} else if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			sender.ID = u.UserID
			if userEntity, ok := e.Users[u.UserID]; ok {
				sender.FirstName = userEntity.FirstName
				sender.LastName = userEntity.LastName
				sender.Username = userEntity.Username
				sender.IsBot = userEntity.Bot
			}
		}
	}

	coreMsg.SenderID = sender.ID

	root := d.getRootContext()
	if root == nil {
		root = context.Background()
	}
	execCtx, cancel := context.WithTimeout(root, 30*time.Second)

	coreCtx := &core.Context{
		Ctx:      execCtx,
		Command:  parsed.Name,
		Args:     parsed.Args,
		RawArgs:  parsed.RawArgs,
		Message:  coreMsg,
		Chat:     chat,
		Sender:   sender,
		Perms:    d.perms,
		Svc:      d.getService(),
		PeerID:   peerInput,
		Resolver: d.getResolver(),
	}

	// Execute command asynchronously with application-scoped context
	go func() {
		defer cancel()
		_ = d.executor.Execute(coreCtx, cmd)
	}()

	return nil
}

func (d *Dispatcher) safeExecuteInterceptor(
	ctx context.Context,
	h MessageHandler,
	e tg.Entities,
	msg *tg.Message,
	isCmd bool,
	cmdName string,
) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("message interceptor panicked", zap.Any("panic", r))
		}
	}()

	interceptorCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := h(interceptorCtx, e, msg, isCmd, cmdName); err != nil {
		d.logger.Warn("message interceptor returned error", zap.Error(err))
	}
}
