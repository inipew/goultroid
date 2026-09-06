package telegram

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"go.uber.org/zap"
)

// Dispatcher processes incoming Telegram updates and routes them to userbot commands.
type Dispatcher struct {
	router         *core.Router
	perms          *core.Permissions
	svc            core.TelegramServicer
	logger         *zap.Logger
	cooldown       *core.CooldownTracker
	executor       *core.CommandExecutor
	selfID         int64
	resolver       core.PeerResolver
	rootCtx        context.Context
	eventBus       *core.EventBus
	albumBuffer    *core.AlbumBuffer
	localizer      core.Localizer
	callbackRouter *callback.Router
	inlineEngine   *inline.Engine

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
		router:      router,
		perms:       perms,
		svc:         svc,
		logger:      logger,
		cooldown:    cooldown,
		executor:    executor,
		albumBuffer: core.NewAlbumBuffer(10 * time.Minute),
	}
}

// Executor returns the underlying CommandExecutor used by this dispatcher.
func (d *Dispatcher) Executor() *core.CommandExecutor {
	return d.executor
}

// SetExecutor sets the CommandExecutor used by this dispatcher.
func (d *Dispatcher) SetExecutor(executor *core.CommandExecutor) {
	if executor != nil {
		d.executor = executor
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

// EventBus returns the domain event bus used by this dispatcher.
func (d *Dispatcher) EventBus() *core.EventBus {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.eventBus
}

// SetEventBus sets the domain event bus. Must be called before RegisterHooks.
func (d *Dispatcher) SetEventBus(bus *core.EventBus) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.eventBus = bus
}

func (d *Dispatcher) getEventBus() *core.EventBus {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.eventBus
}

// AlbumBuffer returns the album aggregator used by this dispatcher.
func (d *Dispatcher) AlbumBuffer() *core.AlbumBuffer {
	return d.albumBuffer
}

// SetLocalizer configures the internationalization provider.
func (d *Dispatcher) SetLocalizer(l core.Localizer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.localizer = l
}

func (d *Dispatcher) getLocalizer() core.Localizer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.localizer
}

// SetCallbackRouter configures the router for button callback queries.
func (d *Dispatcher) SetCallbackRouter(r *callback.Router) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.callbackRouter = r
}

func (d *Dispatcher) getCallbackRouter() *callback.Router {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.callbackRouter
}

// SetInlineEngine configures the inline query evaluation engine.
func (d *Dispatcher) SetInlineEngine(e *inline.Engine) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inlineEngine = e
}

func (d *Dispatcher) getInlineEngine() *inline.Engine {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.inlineEngine
}

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
	bus := d.getEventBus()
	if bus == nil {
		return nil
	}
	bus.Publish(&core.MessagesDeletedEvent{
		At:     time.Now(),
		ChatID: 0, // not available in this update type without additional context
		MsgIDs: update.Messages,
	})
	return nil
}

// OnDeleteChannelMessages handles bulk message deletions in supergroups and channels.
func (d *Dispatcher) OnDeleteChannelMessages(ctx context.Context, e tg.Entities, update *tg.UpdateDeleteChannelMessages) error {
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

// OnBotCallbackQuery handles inline keyboard button callback queries.
func (d *Dispatcher) OnBotCallbackQuery(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
	chatID := extractChatIDFromPeer(update.Peer)
	evt := &core.CallbackQueryEvent{
		At:      time.Now(),
		QueryID: update.QueryID,
		UserID:  update.UserID,
		ChatID:  chatID,
		MsgID:   update.MsgID,
		Data:    update.Data,
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
	evt := &core.CallbackQueryEvent{
		At:      time.Now(),
		QueryID: update.QueryID,
		UserID:  update.UserID,
		Data:    update.Data,
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
	engine := d.getInlineEngine()
	if engine == nil {
		return nil
	}
	return engine.Execute(ctx, d.getService(), update.QueryID, update.UserID, update.Query, update.Offset)
}

// OnMessageReactions handles reaction updates on messages.
func (d *Dispatcher) OnMessageReactions(ctx context.Context, e tg.Entities, update *tg.UpdateMessageReactions) error {
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

func (d *Dispatcher) dispatch(ctx context.Context, e tg.Entities, msg *tg.Message) error {
	parsed, isCmd, err := d.router.Parse(msg.Message)
	if err != nil {
		d.logger.Warn("command parse syntax error", zap.Error(err), zap.String("text", msg.Message))
		return nil
	}
	cmdName := ""
	if isCmd {
		cmdName = parsed.Name
	}

	// Asynchronously cache peer entities in local SQLite for fast offline resolution
	if len(e.Users) > 0 || len(e.Channels) > 0 || len(e.Chats) > 0 {
		resolver := d.getResolver()
		if r, ok := resolver.(*Resolver); ok && r.storage != nil {
			go func() {
				bgCtx := context.Background()
				for _, user := range e.Users {
					if user != nil {
						_ = r.storage.Save(bgCtx, peers.Key{Prefix: "user", ID: user.ID}, peers.Value{AccessHash: user.AccessHash})
						_ = r.storage.SaveEntity(bgCtx, "user", user.ID, user.Username, user.Phone, user.FirstName, user.LastName, "")
					}
				}
				for _, ch := range e.Channels {
					if ch != nil {
						_ = r.storage.Save(bgCtx, peers.Key{Prefix: "channel", ID: ch.ID}, peers.Value{AccessHash: ch.AccessHash})
						_ = r.storage.SaveEntity(bgCtx, "channel", ch.ID, ch.Username, "", "", "", ch.Title)
					}
				}
				for _, chat := range e.Chats {
					if chat != nil {
						_ = r.storage.Save(bgCtx, peers.Key{Prefix: "chat", ID: chat.ID}, peers.Value{AccessHash: 0})
						_ = r.storage.SaveEntity(bgCtx, "chat", chat.ID, "", "", "", "", chat.Title)
					}
				}
			}()
		}
	}

	// Run message interceptors (e.g. AFK, filters).
	d.mu.RLock()
	handlers := make([]MessageHandler, len(d.messageHandlers))
	copy(handlers, d.messageHandlers)
	d.mu.RUnlock()

	for _, h := range handlers {
		d.safeExecuteInterceptor(ctx, h, e, msg, isCmd, cmdName)
	}

	coreMsg := extractCoreMessage(msg)
	if coreMsg.GroupedID != 0 && d.albumBuffer != nil {
		d.albumBuffer.Add(coreMsg)
	}

	if !isCmd {
		return nil
	}

	cmd, exists := d.router.Find(parsed.Name)
	if !exists {
		return nil
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
	}

	// Execute command asynchronously with application-scoped context
	go func() {
		defer cancel()
		_ = d.executor.Execute(coreCtx, cmd)
	}()

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
