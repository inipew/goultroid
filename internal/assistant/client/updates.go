package client

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

const assistantGroupRuleQueueTimeout = 5 * time.Second

type interactionIngressPort interface {
	tryText(context.Context, string, int64, int64, tg.InputPeerClass) (bool, error)
	tryInline(context.Context, []byte, int64, int64, tg.InputBotInlineMessageIDClass) (bool, error)
	tryMessage(context.Context, []byte, int64, int64, tg.InputPeerClass, int64, int) (bool, error)
}

type groupServiceIngress interface {
	Interested(int64, core.GroupServiceKind) bool
	Publish(*core.GroupServiceEvent)
}

type groupRuleIngress interface {
	Interested(int64) bool
	Handle(context.Context, *core.MessageEnvelope) error
}

type groupRuleChatClassifier interface {
	Classify(context.Context, *tg.Message, tg.Entities, tg.InputPeerClass) (core.Chat, error)
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
	GroupRules          groupRuleIngress
	GroupRuleChats      groupRuleChatClassifier
	GlobalPrivileged    func(int64) bool
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

func assistantReplyPeer(message *tg.Message, entities tg.Entities) core.PeerRef {
	if message == nil || message.ReplyTo == nil {
		return core.PeerRef{}
	}
	header, ok := message.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || header == nil || header.ReplyToPeerID == nil {
		return core.PeerRef{}
	}
	var accessHash int64
	switch peer := header.ReplyToPeerID.(type) {
	case *tg.PeerUser:
		if user := entities.Users[peer.UserID]; user != nil {
			accessHash = user.AccessHash
		}
	case *tg.PeerChannel:
		if channel := entities.Channels[peer.ChannelID]; channel != nil {
			accessHash = channel.AccessHash
		}
	}
	ref, err := core.PeerRefFromPeer(header.ReplyToPeerID, accessHash)
	if err != nil {
		return core.PeerRef{}
	}
	return ref
}

func assistantMessageMentions(message *tg.Message) []core.MessageMention {
	if message == nil {
		return nil
	}
	parsed := (&core.Message{Text: message.Message, Entities: message.Entities}).Mentions()
	if len(parsed) == 0 {
		return nil
	}
	mentions := make([]core.MessageMention, 0, len(parsed))
	for _, raw := range parsed {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if userID, err := strconv.ParseInt(value, 10, 64); err == nil && userID > 0 {
			mentions = append(mentions, core.MessageMention{UserID: userID})
			continue
		}
		username := strings.TrimPrefix(value, "@")
		if username != "" {
			mentions = append(mentions, core.MessageMention{Username: username})
		}
	}
	return mentions
}

func assistantMentionsSelf(mentions []core.MessageMention, selfID int64, selfUsername string) bool {
	selfUsername = strings.TrimPrefix(strings.TrimSpace(selfUsername), "@")
	for _, mention := range mentions {
		if selfID > 0 && mention.UserID == selfID {
			return true
		}
		if selfUsername != "" && mention.Username != "" && strings.EqualFold(mention.Username, selfUsername) {
			return true
		}
	}
	return false
}

func assistantCommandMessageContext(
	message *tg.Message,
	entities tg.Entities,
	selfID int64,
	selfUsername string,
) command.MessageContext {
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

	mentions := assistantMessageMentions(message)
	topicID := assistantTopicID(message)
	replyToID := assistantReplyToMessageID(message)
	return command.MessageContext{
		Chat:             chat,
		MessageID:        message.ID,
		ReplyToMessageID: replyToID,
		ReplyPeer:        assistantReplyPeer(message, entities),
		ReplyIsTopicRoot: topicID > 0 && replyToID == topicID,
		TopicID:          topicID,
		Media:            core.ExtractMediaFromTG(message.Media),
		GroupedID:        message.GroupedID,
		Entities:         append([]tg.MessageEntityClass(nil), message.Entities...),
		Self:             core.User{ID: selfID, Username: selfUsername, IsBot: selfID > 0},
		MentionedSelf:    assistantMentionsSelf(mentions, selfID, selfUsername),
		Mentions:         mentions,
	}
}

func assistantGroupRuleEnvelope(
	message *tg.Message,
	entities tg.Entities,
	inputPeer tg.InputPeerClass,
	chat core.Chat,
	senderID int64,
	selfID int64,
	selfUsername string,
) (*core.MessageEnvelope, bool) {
	if message == nil || inputPeer == nil || senderID <= 0 || !(&chat).IsManagerGroup() {
		return nil, false
	}
	messageContext := assistantCommandMessageContext(message, entities, selfID, selfUsername)
	peerRef, err := core.PeerRefFromInputPeer(inputPeer)
	if err != nil || !peerRef.Valid() {
		return nil, false
	}
	sender := core.User{ID: senderID}
	senderVerified := false
	if user := entities.Users[senderID]; user != nil {
		sender.FirstName = user.FirstName
		sender.LastName = user.LastName
		sender.Username = user.Username
		sender.IsBot = user.Bot
		senderVerified = true
	}
	return &core.MessageEnvelope{
		ID:               message.ID,
		ChatID:           chat.ID,
		Peer:             peerRef,
		Chat:             chat,
		Sender:           sender,
		Text:             message.Message,
		Date:             time.Unix(int64(message.Date), 0),
		ReplyToID:        messageContext.ReplyToMessageID,
		ReplyPeer:        messageContext.ReplyPeer,
		TopicID:          messageContext.TopicID,
		GroupedID:        message.GroupedID,
		Outgoing:         message.Out,
		IsCommand:        false,
		SenderVerified:   senderVerified,
		SenderSelf:       senderID == selfID,
		Self:             messageContext.Self,
		Mentioned:        messageContext.MentionedSelf,
		ReplyIsTopicRoot: messageContext.ReplyIsTopicRoot,
		Mentions:         append([]core.MessageMention(nil), messageContext.Mentions...),
		Media:            core.SummarizeMedia(messageContext.Media),
	}, true
}

func submitAssistantGroupRules(
	ctx context.Context,
	message *tg.Message,
	entities tg.Entities,
	senderID int64,
	deps UpdateHandlerDeps,
	logger *zap.Logger,
) {
	if message == nil || deps.GroupRules == nil || deps.GroupRuleChats == nil ||
		deps.Tasks == nil || deps.Resolver == nil {
		return
	}
	chatID := extractChatID(message.PeerID)
	if chatID <= 0 {
		return
	}
	_, err := deps.Tasks.Submit(ctx, tasks.WorkSpec{
		ID:               tasks.TaskID(fmt.Sprintf("asst:rules:%d:%d", chatID, message.ID)),
		QuotaOwner:       tasks.OwnerID(fmt.Sprintf("telegram:chat:%d", chatID)),
		Pool:             tasks.PoolID("interactive"),
		Class:            tasks.PriorityInteractive,
		OrderingKey:      core.GroupOrderingKey(chatID, assistantTopicID(message)),
		QueueDeadline:    time.Now().Add(assistantGroupRuleQueueTimeout),
		ExecutionTimeout: 5 * time.Second,
		Handler: func(taskCtx context.Context) error {
			inputPeer, resolveErr := deps.Resolver.Resolve(
				taskCtx,
				message.PeerID,
				senderID,
				entities,
			)
			if resolveErr != nil || inputPeer == nil {
				if resolveErr == nil {
					resolveErr = core.ErrUnavailable
				}
				return fmt.Errorf("assistant group rules resolve peer: %w", resolveErr)
			}
			chat, classifyErr := deps.GroupRuleChats.Classify(
				taskCtx,
				message,
				entities,
				inputPeer,
			)
			if classifyErr != nil {
				if errors.Is(classifyErr, core.ErrGroupOnly) {
					return nil
				}
				return classifyErr
			}
			selfID := int64(0)
			if deps.SelfID != nil {
				selfID = deps.SelfID()
			}
			selfUsername := ""
			if deps.CmdRouter != nil {
				selfUsername = deps.CmdRouter.BotUsername()
			}
			envelope, ok := assistantGroupRuleEnvelope(
				message,
				entities,
				inputPeer,
				chat,
				senderID,
				selfID,
				selfUsername,
			)
			if !ok {
				return nil
			}
			return deps.GroupRules.Handle(taskCtx, envelope)
		},
	})
	if err != nil && logger != nil {
		logger.Warn("assistant: group rule admission rejected",
			zap.Int64("chat_id", chatID),
			zap.Int("message_id", message.ID),
			zap.Error(err),
		)
	}
}

// InlineQueryExecutor is the Assistant-facing subset of the shared inline engine.
type InlineQueryExecutor interface {
	Prepare(string) (inlineservice.PreparedQuery, error)
	ExecuteWithPeerType(context.Context, core.TelegramServicer, int64, int64, string, string, tg.InlineQueryPeerTypeClass) error
	ExecutePreparedWithPeerType(context.Context, core.TelegramServicer, int64, int64, inlineservice.PreparedQuery, string, tg.InlineQueryPeerTypeClass) error
}

type assistantGroupServiceChat struct {
	id         int64
	title      string
	accessHash int64
	supergroup bool
}

func assistantGroupServiceChatMeta(
	message *tg.MessageService,
	entities tg.Entities,
) (assistantGroupServiceChat, bool) {
	if message == nil {
		return assistantGroupServiceChat{}, false
	}
	switch peer := message.PeerID.(type) {
	case *tg.PeerChat:
		meta := assistantGroupServiceChat{id: peer.ChatID}
		if chat := entities.Chats[peer.ChatID]; chat != nil {
			meta.title = chat.Title
		}
		return meta, meta.id > 0
	case *tg.PeerChannel:
		meta := assistantGroupServiceChat{id: peer.ChannelID, supergroup: true}
		if channel := entities.Channels[peer.ChannelID]; channel != nil {
			// Entity metadata is authoritative when present. Broadcast channels
			// never enter the Assistant group-event manager plane.
			if !channel.Megagroup {
				return assistantGroupServiceChat{}, false
			}
			meta.title = channel.Title
			meta.accessHash = channel.AccessHash
		}
		// Missing entity/access hash is not a reason to lose an enabled
		// supergroup event. The peer is recovered lazily after the interest hit.
		return meta, meta.id > 0
	default:
		return assistantGroupServiceChat{}, false
	}
}

func resolveAssistantGroupServicePeer(
	ctx context.Context,
	message *tg.MessageService,
	entities tg.Entities,
	chat assistantGroupServiceChat,
	resolver peer.Resolver,
) tg.InputPeerClass {
	if !chat.supergroup {
		return &tg.InputPeerChat{ChatID: chat.id}
	}
	if chat.accessHash != 0 {
		return &tg.InputPeerChannel{ChannelID: chat.id, AccessHash: chat.accessHash}
	}
	if resolver == nil || message == nil {
		return nil
	}
	resolved, err := resolver.Resolve(ctx, message.PeerID, 0, entities)
	if err != nil {
		return nil
	}
	channel, ok := resolved.(*tg.InputPeerChannel)
	if !ok || channel.ChannelID != chat.id || channel.AccessHash == 0 {
		return nil
	}
	return channel
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

const maxAssistantGroupServiceUsers = 64

func handleAssistantGroupService(
	ctx context.Context,
	message *tg.MessageService,
	entities tg.Entities,
	deps UpdateHandlerDeps,
) {
	if message == nil || deps.GroupEvents == nil {
		return
	}
	chat, ok := assistantGroupServiceChatMeta(message, entities)
	if !ok {
		return
	}
	kind, userIDs, ok := assistantGroupServiceKind(message)
	if !ok || !deps.GroupEvents.Interested(chat.id, kind) {
		return
	}
	peer := resolveAssistantGroupServicePeer(ctx, message, entities, chat, deps.Resolver)
	if peer == nil {
		return
	}

	selfID := int64(0)
	if deps.SelfID != nil {
		selfID = deps.SelfID()
	}
	capacity := len(userIDs)
	if capacity > maxAssistantGroupServiceUsers {
		capacity = maxAssistantGroupServiceUsers
	}
	seen := make(map[int64]struct{}, capacity)
	users := make([]core.GroupServiceUser, 0, capacity)
	userCount := 0
	for _, userID := range userIDs {
		if userID <= 0 || userID == selfID {
			continue
		}
		if len(users) < maxAssistantGroupServiceUsers {
			if _, duplicate := seen[userID]; duplicate {
				continue
			}
			seen[userID] = struct{}{}
			users = append(users, assistantGroupServiceUser(userID, entities))
			userCount++
			continue
		}
		// Once the materialized set reaches its hard cap, keep only an
		// allocation-free reported count. Telegram service actions normally
		// contain unique IDs; duplicates beyond the cap may only overstate the
		// display count, never expand retained event state.
		userCount++
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
		ChatID:    chat.id,
		ChatTitle: chat.title,
		Peer:      peer,
		MessageID: message.ID,
		ActorID:   actorID,
		UserCount: userCount,
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
			handleAssistantGroupService(ctx, service, e, deps)
			return nil
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
		commandToken, slashCommand := assistantSlashCommand(msg.Message)
		groupRuleCandidate := false
		if !privateChat && !slashCommand {
			if strings.TrimSpace(msg.Message) == "" || deps.GroupRules == nil ||
				deps.GroupRuleChats == nil {
				return nil
			}
			switch peer := msg.PeerID.(type) {
			case *tg.PeerChat:
				// Basic groups are authoritative from the peer shape alone.
			case *tg.PeerChannel:
				if channel := e.Channels[peer.ChannelID]; channel != nil && !channel.Megagroup {
					return nil
				}
			default:
				return nil
			}
			// Owner/Sudo bypass is an automation policy only. It does not
			// manufacture Telegram group authority, but it can safely skip the
			// P7-I data plane before interest/cache/task work.
			if deps.GlobalPrivileged != nil && deps.GlobalPrivileged(senderID) {
				return nil
			}
			if !deps.GroupRules.Interested(chatID) {
				return nil
			}
			groupRuleCandidate = true
		}

		if deps.CacheEntities != nil {
			deps.CacheEntities(e)
		}
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
				assistantCommandMessageContext(msg, e, func() int64 {
					if deps.SelfID != nil {
						return deps.SelfID()
					}
					return 0
				}(), deps.CmdRouter.BotUsername()),
				deps.Interaction,
			)
			if err != nil && !errors.Is(err, command.ErrUnknownCommand) {
				logger.Warn("assistant: command error", zap.Error(err), zap.Int64("sender_id", senderID))
			}
		}

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

		if groupRuleCandidate {
			submitAssistantGroupRules(ctx, msg, e, senderID, deps, logger)
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
