package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/pmrelay"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type interactionIngressPort interface {
	tryText(context.Context, string, int64, int64, tg.InputPeerClass) (bool, error)
	tryInline(context.Context, []byte, int64, int64, tg.InputBotInlineMessageIDClass) (bool, error)
	tryMessage(context.Context, []byte, int64, int64, tg.InputPeerClass, int64, int) (bool, error)
}

type groupServiceIngress interface {
	Interested(int64, core.GroupServiceKind) bool
	Publish(*core.GroupServiceEvent)
}

type UpdateHandlerDeps struct {
	Logger              *zap.Logger
	RateLimiter         RateLimiter
	Resolver            peer.Resolver
	CmdRouter           *command.Router
	CallbackDispatcher  CoreCallbackDispatcher
	CallbackDeduper     *callbackQueryDeduper
	Interaction         *interaction.ClientInteraction
	CacheEntities       func(e tg.Entities)
	IsShuttingDown      func() bool
	InlineEngine        InlineQueryExecutor
	InlineService       core.TelegramServicer
	Tasks               tasks.Client
	PluginScopeResolver func(string) (tasks.ScopeIdentity, bool)
	InteractionIngress  interactionIngressPort
	RelayIngress        relayMessageIngress
	AudienceRegistry    pmrelay.AudienceRegistry
	GroupEvents         groupServiceIngress
	SelfID              func() int64
}

func assistantSlashCommand(text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", false
	}
	return strings.ToLower(fields[0]), true
}

func assistantReplyToMessageID(message *tg.Message) int {
	if message == nil || message.ReplyTo == nil {
		return 0
	}
	header, ok := message.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || header == nil {
		return 0
	}
	return header.ReplyToMsgID
}

func assistantTopicID(message *tg.Message) int {
	if message == nil || message.ReplyTo == nil {
		return 0
	}
	header, ok := message.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || header == nil || (!header.ForumTopic && header.ReplyToTopID == 0) {
		return 0
	}
	if header.ReplyToTopID != 0 {
		return header.ReplyToTopID
	}
	return header.ReplyToMsgID
}

func assistantCommandMessageContext(message *tg.Message, entities tg.Entities) command.MessageContext {
	if message == nil {
		return command.MessageContext{}
	}

	chat := core.Chat{ID: extractChatID(message.PeerID)}
	switch peer := message.PeerID.(type) {
	case *tg.PeerUser:
		chat.Type = string(core.ChatKindPrivate)
		if user := entities.Users[peer.UserID]; user != nil {
			chat.Title = strings.TrimSpace(user.FirstName + " " + user.LastName)
			chat.Username = user.Username
			chat.AccessHash = user.AccessHash
		}
	case *tg.PeerChat:
		chat.Type = string(core.ChatKindGroup)
		if group := entities.Chats[peer.ChatID]; group != nil {
			chat.Title = group.Title
		}
	case *tg.PeerChannel:
		// PeerChannel is ambiguous without entity metadata. Keep it as a
		// broadcast channel by default so manager-plane GroupOnly commands fail
		// closed instead of accidentally treating every channel peer as a group.
		chat.Type = string(core.ChatKindChannel)
		if channel := entities.Channels[peer.ChannelID]; channel != nil {
			chat.Title = channel.Title
			chat.Username = channel.Username
			chat.AccessHash = channel.AccessHash
			if channel.Megagroup {
				chat.Type = string(core.ChatKindSupergroup)
			}
		}
	}

	return command.MessageContext{
		Chat:             chat,
		MessageID:        message.ID,
		ReplyToMessageID: assistantReplyToMessageID(message),
		TopicID:          assistantTopicID(message),
	}
}

// InlineQueryExecutor is the Assistant-facing subset of the shared inline engine.
type InlineQueryExecutor interface {
	Prepare(string) (inlineservice.PreparedQuery, error)
	ExecuteWithPeerType(context.Context, core.TelegramServicer, int64, int64, string, string, tg.InlineQueryPeerTypeClass) error
	ExecutePreparedWithPeerType(context.Context, core.TelegramServicer, int64, int64, inlineservice.PreparedQuery, string, tg.InlineQueryPeerTypeClass) error
}

func assistantGroupServicePeer(
	message *tg.MessageService,
	entities tg.Entities,
) (tg.InputPeerClass, int64, string, bool) {
	if message == nil {
		return nil, 0, "", false
	}
	switch peer := message.PeerID.(type) {
	case *tg.PeerChat:
		title := ""
		if chat := entities.Chats[peer.ChatID]; chat != nil {
			title = chat.Title
		}
		return &tg.InputPeerChat{ChatID: peer.ChatID}, peer.ChatID, title, true
	case *tg.PeerChannel:
		channel := entities.Channels[peer.ChannelID]
		if channel == nil || !channel.Megagroup || channel.AccessHash == 0 {
			return nil, 0, "", false
		}
		return &tg.InputPeerChannel{
			ChannelID:  peer.ChannelID,
			AccessHash: channel.AccessHash,
		}, peer.ChannelID, channel.Title, true
	default:
		return nil, 0, "", false
	}
}

func assistantGroupServiceKind(
	message *tg.MessageService,
) (core.GroupServiceKind, []int64, bool) {
	if message == nil {
		return "", nil, false
	}
	switch action := message.Action.(type) {
	case *tg.MessageActionChatAddUser:
		if len(action.Users) == 0 {
			return "", nil, false
		}
		return core.GroupServiceMemberJoined, action.Users, true
	case *tg.MessageActionChatJoinedByLink, *tg.MessageActionChatJoinedByRequest:
		if from, ok := message.FromID.(*tg.PeerUser); ok && from.UserID > 0 {
			return core.GroupServiceMemberJoined, []int64{from.UserID}, true
		}
	case *tg.MessageActionChatDeleteUser:
		if action.UserID > 0 {
			return core.GroupServiceMemberLeft, []int64{action.UserID}, true
		}
	}
	return "", nil, false
}

func assistantGroupServiceUser(userID int64, entities tg.Entities) core.GroupServiceUser {
	user := entities.Users[userID]
	if user == nil {
		return core.GroupServiceUser{ID: userID}
	}
	return core.GroupServiceUser{
		ID:        userID,
		FirstName: user.FirstName,
		LastName:  user.LastName,
		Username:  user.Username,
		IsBot:     user.Bot,
	}
}

func handleAssistantGroupService(
	message *tg.MessageService,
	entities tg.Entities,
	deps UpdateHandlerDeps,
) {
	if message == nil || deps.GroupEvents == nil {
		return
	}
	peer, chatID, chatTitle, ok := assistantGroupServicePeer(message, entities)
	if !ok {
		return
	}
	kind, userIDs, ok := assistantGroupServiceKind(message)
	if !ok || !deps.GroupEvents.Interested(chatID, kind) {
		return
	}

	selfID := int64(0)
	if deps.SelfID != nil {
		selfID = deps.SelfID()
	}
	seen := make(map[int64]struct{}, len(userIDs))
	users := make([]core.GroupServiceUser, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 || userID == selfID {
			continue
		}
		if _, duplicate := seen[userID]; duplicate {
			continue
		}
		seen[userID] = struct{}{}
		users = append(users, assistantGroupServiceUser(userID, entities))
	}
	if len(users) == 0 {
		return
	}

	var actorID int64
	if from, ok := message.FromID.(*tg.PeerUser); ok {
		actorID = from.UserID
	}
	deps.GroupEvents.Publish(&core.GroupServiceEvent{
		At:        time.Unix(int64(message.Date), 0),
		Kind:      kind,
		ChatID:    chatID,
		ChatTitle: chatTitle,
		Peer:      peer,
		MessageID: message.ID,
		ActorID:   actorID,
		Users:     users,
	})
}

func RegisterUpdateHandlers(dispatcher *tg.UpdateDispatcher, deps UpdateHandlerDeps) {
	if dispatcher == nil {
		return
	}
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	handleNewMessage := func(ctx context.Context, e tg.Entities, message tg.MessageClass) error {
		if deps.IsShuttingDown != nil && deps.IsShuttingDown() {
			return nil
		}
		if service, ok := message.(*tg.MessageService); ok {
			// P7-H service updates are fully classified from the update's entity
			// snapshot. Inactive chats return before touching the global peer
			// cache, TaskEngine, EventBus queue, or interaction pipeline.
			handleAssistantGroupService(service, e, deps)
			return nil
		}
		if deps.CacheEntities != nil {
			deps.CacheEntities(e)
		}
		msg, ok := message.(*tg.Message)
		if !ok || msg.Out {
			return nil
		}
		var senderID int64
		if user, ok := msg.FromID.(*tg.PeerUser); ok {
			senderID = user.UserID
		} else if user, ok := msg.PeerID.(*tg.PeerUser); ok {
			senderID = user.UserID
		}
		if senderID == 0 {
			return nil
		}

		chatID := extractChatID(msg.PeerID)
		_, privateChat := msg.PeerID.(*tg.PeerUser)
		plainTextRelay := msg.Media == nil && strings.TrimSpace(msg.Message) != ""
		relayMessage := pmrelay.IngressMessage{
			SenderID:         senderID,
			ChatID:           chatID,
			MessageID:        msg.ID,
			ReplyToMessageID: assistantReplyToMessageID(msg),
		}

		resolvePeer := func() tg.InputPeerClass {
			if deps.Resolver == nil {
				return nil
			}
			inputPeer, err := deps.Resolver.Resolve(ctx, msg.PeerID, senderID, e)
			if err != nil || inputPeer == nil {
				logger.Warn("assistant: sender access hash unavailable for command/interaction path", zap.Int64("sender_id", senderID), zap.Error(err))
				return nil
			}
			return inputPeer
		}
		dispatchCommand := func(inputPeer tg.InputPeerClass) {
			if inputPeer == nil || deps.Interaction == nil || deps.CmdRouter == nil {
				return
			}
			if deps.RateLimiter != nil && !deps.RateLimiter.Allow(senderID, "command") {
				logger.Warn("assistant: rate limit exceeded for command", zap.Int64("sender_id", senderID))
				return
			}
			err := deps.CmdRouter.DispatchMessageContext(
				ctx,
				senderID,
				inputPeer,
				msg.Message,
				assistantCommandMessageContext(msg, e),
				deps.Interaction,
			)
			if err != nil && !errors.Is(err, command.ErrUnknownCommand) {
				logger.Warn("assistant: command error", zap.Error(err), zap.Int64("sender_id", senderID))
			}
		}

		commandToken, slashCommand := assistantSlashCommand(msg.Message)
		if slashCommand {
			commandName := commandToken
			if deps.CmdRouter != nil {
				var targeted bool
				commandName, targeted = deps.CmdRouter.NormalizeCommandToken(commandToken)
				if !targeted {
					// A slash command targeted at another bot remains command-plane
					// traffic, but must not reach our /cancel control or PM Relay.
					return nil
				}
			} else if strings.Contains(commandToken, "@") {
				// Without authenticated bot identity, suffixed commands fail closed.
				return nil
			}
			inputPeer := resolvePeer()
			if commandName == "/cancel" && deps.InteractionIngress != nil && inputPeer != nil {
				handled, hErr := deps.InteractionIngress.tryText(ctx, msg.Message, senderID, chatID, inputPeer)
				if hErr != nil {
					logger.Warn("assistant: interaction text input dispatch failed", zap.Error(hErr), zap.Int64("sender_id", senderID))
					if feedback := interactionTextInputErrorMessage(hErr); feedback != "" && deps.Interaction != nil {
						_, _ = deps.Interaction.SendMessage(ctx, inputPeer, feedback, nil)
					}
					return nil
				}
				if handled {
					return nil
				}
			}
			// Slash-prefixed messages are command/control-plane traffic. Unknown
			// commands fail closed here and are never reclassified as PM relay.
			dispatchCommand(inputPeer)
			return nil
		}

		// Owner replies to a durable relay mapping outrank generic AwaitInput.
		// This keeps a Settings/MyXL input claim from consuming an explicit reply
		// to a visitor conversation.
		if privateChat && deps.RelayIngress != nil {
			if handled, relayErr := deps.RelayIngress.tryOwnerReply(ctx, relayMessage); handled {
				if relayErr != nil {
					logger.Warn("assistant: PM relay owner reply admission failed",
						zap.Error(relayErr),
						zap.Int64("sender_id", senderID),
						zap.Int("message_id", msg.ID),
					)
				}
				return nil
			}
		}

		// Generic a2 input is attempted only when its presentation/resolution
		// dependencies are available. PM Relay is an independent data-plane
		// fallback and must not disappear merely because the interaction path
		// cannot resolve a peer or is temporarily unavailable.
		if privateChat && deps.Resolver != nil && deps.Interaction != nil {
			inputPeer := resolvePeer()
			if inputPeer != nil && deps.InteractionIngress != nil {
				handled, hErr := deps.InteractionIngress.tryText(ctx, msg.Message, senderID, chatID, inputPeer)
				if hErr != nil {
					logger.Warn("assistant: interaction text input dispatch failed", zap.Error(hErr), zap.Int64("sender_id", senderID))
					if feedback := interactionTextInputErrorMessage(hErr); feedback != "" {
						_, _ = deps.Interaction.SendMessage(ctx, inputPeer, feedback, nil)
					}
					// Classification failure is terminal for this update. Only a
					// clean "not handled" result may fall through to PM Relay.
					return nil
				}
				if handled {
					return nil
				}
			}
		}

		// PM Relay is the final private-message data-plane fallback. Prepare is
		// read-only; execution is admitted through TaskEngine and revalidated in
		// RelayIngress before durable Telegram delivery.
		if privateChat && plainTextRelay && deps.RelayIngress != nil {
			if handled, relayErr := deps.RelayIngress.tryVisitor(ctx, relayMessage); handled {
				if relayErr != nil {
					logger.Warn("assistant: PM relay visitor admission failed",
						zap.Error(relayErr),
						zap.Int64("sender_id", senderID),
						zap.Int("message_id", msg.ID),
					)
				}
				return nil
			}
		}
		return nil
	}
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
		return handleNewMessage(ctx, e, update.Message)
	})
	dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewChannelMessage) error {
		return handleNewMessage(ctx, e, update.Message)
	})
	dispatcher.OnBotInlineQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineQuery) error {
		if deps.IsShuttingDown != nil && deps.IsShuttingDown() {
			if deps.InlineService != nil {
				_ = deps.InlineService.AnswerInlineQueryOptions(ctx, update.QueryID, nil, core.InlineAnswerOptions{CacheTime: 1, Private: true})
			}
			return nil
		}
		if deps.CacheEntities != nil {
			deps.CacheEntities(e)
		}
		if deps.InlineEngine == nil || deps.InlineService == nil {
			logger.Warn("assistant: inline query service unavailable", zap.Int64("query_id", update.QueryID))
			if deps.InlineService != nil {
				_ = deps.InlineService.AnswerInlineQueryOptions(ctx, update.QueryID, nil, core.InlineAnswerOptions{CacheTime: 1, Private: true})
			}
			return nil
		}
		if deps.Tasks == nil {
			logger.Warn("assistant: inline query execution unavailable", zap.Int64("query_id", update.QueryID))
			return deps.InlineService.AnswerInlineQueryOptions(ctx, update.QueryID, nil, core.InlineAnswerOptions{CacheTime: 1, Private: true})
		}
		var prepared inlineservice.PreparedQuery
		var prepareErr error
		if contextual, ok := deps.InlineEngine.(interface {
			PrepareContext(context.Context, string) (inlineservice.PreparedQuery, error)
		}); ok {
			prepared, prepareErr = contextual.PrepareContext(ctx, update.Query)
		} else {
			prepared, prepareErr = deps.InlineEngine.Prepare(update.Query)
		}
		var scope tasks.ScopeIdentity
		var resources []tasks.ResourceRequirement
		pool := tasks.PoolID("interactive")
		executionTimeout := 5 * time.Second
		run := func(taskCtx context.Context) error {
			return deps.InlineEngine.ExecuteWithPeerType(taskCtx, deps.InlineService, update.QueryID, update.UserID, update.Query, update.Offset, update.PeerType)
		}
		if prepareErr == nil {
			scope = prepared.Scope()
			resources = prepared.Resources()
			for _, requirement := range resources {
				if requirement.Name == "media" && requirement.Amount > 0 {
					pool = tasks.PoolID("general")
					// SavedResponse media is bounded to 32 MiB, but upload may
					// legitimately exceed the text-only inline budget.
					executionTimeout = 30 * time.Second
					break
				}
			}
			run = func(taskCtx context.Context) error {
				return deps.InlineEngine.ExecutePreparedWithPeerType(taskCtx, deps.InlineService, update.QueryID, update.UserID, prepared, update.Offset, update.PeerType)
			}
		} else if !errors.Is(prepareErr, inlineservice.ErrNoMatchingHandler) {
			logger.Warn("assistant: inline query preparation failed", zap.Int64("query_id", update.QueryID), zap.Error(prepareErr))
			return deps.InlineService.AnswerInlineQueryOptions(ctx, update.QueryID, nil, core.InlineAnswerOptions{CacheTime: 1, Private: true})
		}
		baseRun := run
		run = func(taskCtx context.Context) error {
			if err := baseRun(taskCtx); err != nil {
				return err
			}
			touchAssistantAudience(taskCtx, deps.AudienceRegistry, update.UserID, pmrelay.AudienceSourceInline, logger)
			return nil
		}
		_, err := deps.Tasks.Submit(ctx, tasks.WorkSpec{
			ID:               tasks.TaskID(fmt.Sprintf("asst:inline:%d", update.QueryID)),
			Scope:            scope,
			QuotaOwner:       tasks.OwnerID(fmt.Sprintf("telegram:user:%d", update.UserID)),
			Pool:             pool,
			Class:            tasks.PriorityInteractive,
			OrderingKey:      fmt.Sprintf("inline:%d", update.QueryID),
			ExecutionTimeout: executionTimeout,
			Resources:        append([]tasks.ResourceRequirement(nil), resources...),
			Handler:          run,
		})
		if err != nil {
			logger.Warn("assistant: inline query admission rejected", zap.Int64("query_id", update.QueryID), zap.Error(err))
			_ = deps.InlineService.AnswerInlineQueryOptions(ctx, update.QueryID, nil, core.InlineAnswerOptions{CacheTime: 1, Private: true})
		}
		return nil
	})
	dispatcher.OnInlineBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateInlineBotCallbackQuery) error {
		if deps.IsShuttingDown != nil && deps.IsShuttingDown() {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Bot is shutting down. Please retry later.", true)
			}
			return nil
		}
		if deps.CacheEntities != nil {
			deps.CacheEntities(e)
		}
		inlineTarget := interaction.NewInlineTarget(update.QueryID, update.MsgID, update.ChatInstance)
		if isInteractionCallback(update.Data) {
			if deps.CallbackDeduper != nil && !deps.CallbackDeduper.Admit(update.QueryID, time.Now()) {
				if deps.Interaction != nil {
					_ = deps.Interaction.Answer(ctx, update.QueryID, "", false)
				}
				return nil
			}
			if deps.RateLimiter != nil && !deps.RateLimiter.Allow(update.UserID, "inline") {
				if deps.Interaction != nil {
					_ = deps.Interaction.Answer(ctx, update.QueryID, "Too many requests. Please wait.", true)
				}
				return nil
			}
			if deps.InteractionIngress == nil {
				if deps.Interaction != nil {
					_ = deps.Interaction.Answer(ctx, update.QueryID, "Interaction service unavailable.", false)
				}
				return nil
			}
			_, err := deps.InteractionIngress.tryInline(ctx, update.Data, update.UserID, update.QueryID, update.MsgID)
			if err != nil {
				logger.Warn("assistant: interaction inline callback dispatch failed", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID))
			}
			return nil
		}
		if deps.Interaction == nil {
			return nil
		}
		evt := &core.CallbackQueryEvent{
			At:      time.Now(),
			QueryID: update.QueryID,
			UserID:  update.UserID,
			Data:    update.Data,
			Origin:  core.CallbackOriginInline,
			Target: core.CallbackTarget{
				Origin:       core.CallbackOriginInline,
				InlineID:     update.MsgID,
				ChatInstance: update.ChatInstance,
			},
			ChatInstance: update.ChatInstance,
		}
		svc := newAssistantInlineCallbackServicer(update.QueryID, inlineTarget, deps.Interaction.AsInline())
		if err := dispatchCoreCallback(ctx, deps.CallbackDispatcher, deps.Tasks, deps.PluginScopeResolver, deps.CallbackDeduper, evt, svc, logger); err != nil {
			logger.Warn("assistant: inline callback dispatch failed", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID))
		}
		return nil
	})
	dispatcher.OnBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
		if deps.IsShuttingDown != nil && deps.IsShuttingDown() {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Bot is shutting down. Please retry later.", true)
			}
			return nil
		}
		if deps.CacheEntities != nil {
			deps.CacheEntities(e)
		}
		var inputPeer tg.InputPeerClass
		if deps.Resolver != nil {
			var err error
			inputPeer, err = deps.Resolver.Resolve(ctx, update.Peer, update.UserID, e)
			if err != nil {
				if deps.Interaction != nil {
					_ = deps.Interaction.Answer(ctx, update.QueryID, "Unable to resolve chat. Please retry.", false)
				}
				return nil
			}
		}
		target := interaction.NewMessageTarget(inputPeer, update.MsgID, extractChatID(update.Peer), update.ChatInstance)
		if isInteractionCallback(update.Data) {
			if deps.CallbackDeduper != nil && !deps.CallbackDeduper.Admit(update.QueryID, time.Now()) {
				if deps.Interaction != nil {
					_ = deps.Interaction.Answer(ctx, update.QueryID, "", false)
				}
				return nil
			}
			if deps.RateLimiter != nil && !deps.RateLimiter.Allow(update.UserID, "callback") {
				if deps.Interaction != nil {
					_ = deps.Interaction.Answer(ctx, update.QueryID, "Too many requests. Please wait.", true)
				}
				return nil
			}
			if deps.InteractionIngress == nil {
				if deps.Interaction != nil {
					_ = deps.Interaction.Answer(ctx, update.QueryID, "Interaction service unavailable.", false)
				}
				return nil
			}
			_, err := deps.InteractionIngress.tryMessage(ctx, update.Data, update.UserID, update.QueryID, inputPeer, extractChatID(update.Peer), update.MsgID)
			if err != nil {
				logger.Warn("assistant: interaction callback dispatch failed", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID))
			}
			return nil
		}
		if deps.Interaction == nil {
			return nil
		}
		evt := &core.CallbackQueryEvent{
			At:      time.Now(),
			QueryID: update.QueryID,
			UserID:  update.UserID,
			ChatID:  target.ChatID(),
			MsgID:   target.MessageID(),
			Data:    update.Data,
			Origin:  core.CallbackOriginMessage,
			Target: core.CallbackTarget{
				Origin:       core.CallbackOriginMessage,
				Peer:         target.Peer(),
				MessageID:    target.MessageID(),
				ChatInstance: target.ChatInstance(),
			},
			ChatInstance: target.ChatInstance(),
		}
		svc := newAssistantCallbackServicer(update.QueryID, target, deps.Interaction)
		if err := dispatchCoreCallback(ctx, deps.CallbackDispatcher, deps.Tasks, deps.PluginScopeResolver, deps.CallbackDeduper, evt, svc, logger); err != nil {
			logger.Warn("assistant: callback dispatch failed", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID))
		}
		return nil
	})
}

func extractChatID(p tg.PeerClass) int64 {
	if p == nil {
		return 0
	}
	switch peer := p.(type) {
	case *tg.PeerUser:
		return peer.UserID
	case *tg.PeerChat:
		return peer.ChatID
	case *tg.PeerChannel:
		return peer.ChannelID
	default:
		return 0
	}
}
