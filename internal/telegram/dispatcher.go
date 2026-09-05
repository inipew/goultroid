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
	router *core.Router
	perms  *core.Permissions
	svc    core.TelegramServicer
	logger *zap.Logger
	selfID int64
	mu     sync.RWMutex
}

// NewDispatcher creates a new Dispatcher instance.
func NewDispatcher(
	router *core.Router,
	perms *core.Permissions,
	svc core.TelegramServicer,
	logger *zap.Logger,
) *Dispatcher {
	return &Dispatcher{
		router: router,
		perms:  perms,
		svc:    svc,
		logger: logger,
	}
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
	parsed, ok := d.router.Parse(msg.Message)
	if !ok {
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

	if msg.ReplyTo != nil {
		if header, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok {
			coreMsg.ReplyToID = header.ReplyToMsgID
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
		core.TimeoutMiddleware(30*time.Second),
	)

	handler := chain.Then(cmd.Handler)

	// Execute command concurrently
	go func() {
		if err := handler(coreCtx); err != nil {
			if errors.Is(err, core.ErrPermissionDenied) {
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
