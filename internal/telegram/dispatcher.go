package telegram

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// Dispatcher processes incoming Telegram updates and routes them to userbot commands.
type Dispatcher struct {
	router   *core.Router
	perms    *core.Permissions
	svc      core.TelegramServicer
	logger   *zap.Logger
	cooldown *core.CooldownTracker
	selfID          int64
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
	return &Dispatcher{
		router:   router,
		perms:    perms,
		svc:      svc,
		logger:   logger,
		cooldown: core.NewCooldownTracker(),
	}
}

// AddMessageHandler registers an interceptor for raw message processing (e.g. AFK, filters).
func (d *Dispatcher) AddMessageHandler(h MessageHandler) {
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

	// Run message interceptors (e.g. AFK, filters)
	d.mu.RLock()
	handlers := make([]MessageHandler, len(d.messageHandlers))
	copy(handlers, d.messageHandlers)
	d.mu.RUnlock()

	for _, h := range handlers {
		if err := h(ctx, e, msg, isCmd, cmdName); err != nil {
			d.logger.Warn("message handler returned error", zap.Error(err))
		}
	}

	if !isCmd {
		return nil
	}

	cmd, exists := d.router.Find(parsed.Name)
	if !exists {
		return nil
	}

	coreMsg := &core.Message{
		ID:   msg.ID,
		Text: msg.Message,
		Date: time.Unix(int64(msg.Date), 0),
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
		if u, ok := e.Users[p.UserID]; ok {
			chat.Username = u.Username
			chat.Title = u.FirstName + " " + u.LastName
			peerInput = &tg.InputPeerUser{UserID: p.UserID, AccessHash: u.AccessHash}
		} else if p.UserID == d.getSelfID() {
			peerInput = &tg.InputPeerSelf{}
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
		chat.Type = "channel"
		if ch, ok := e.Channels[p.ChannelID]; ok {
			chat.Title = ch.Title
			chat.Username = ch.Username
			if ch.Megagroup {
				chat.Type = "supergroup"
			}
			peerInput = &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: ch.AccessHash}
		}
	}

	if peerInput == nil {
		if msg.Out {
			peerInput = &tg.InputPeerSelf{}
		}
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

	coreCtx := &core.Context{
		Ctx:     ctx,
		Command: parsed.Name,
		Args:    parsed.Args,
		RawArgs: parsed.RawArgs,
		Message: coreMsg,
		Chat:    chat,
		Sender:  sender,
		Perms:   d.perms,
		Svc:     d.getService(),
		PeerID:  peerInput,
	}

	chain := core.NewChain(
		core.RecoveryMiddleware(d.logger),
		core.LoggingMiddleware(d.logger),
		core.PermissionMiddleware(cmd),
		core.FilterMiddleware(cmd),
		core.CooldownMiddleware(cmd, d.cooldown),
		core.TimeoutMiddleware(30*time.Second),
	)

	handler := chain.Then(cmd.Handler)

	// Execute command concurrently
	go func() {
		if err := handler(coreCtx); err != nil {
			if errors.Is(err, core.ErrPermissionDenied) || errors.Is(err, core.ErrCooldownActive) {
				return
			}
			if errors.Is(err, core.ErrGroupOnly) || errors.Is(err, core.ErrPrivateOnly) || errors.Is(err, core.ErrReplyRequired) {
				_ = coreCtx.Reply("⚠️ " + err.Error())
				return
			}
			if d.logger != nil {
				d.logger.Error("command failed",
					zap.String("command", parsed.Name),
					zap.Error(err),
				)
			}
		}
	}()

	return nil
}
