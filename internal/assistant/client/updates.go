package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

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
	InteractionIngress  *interactionIngress
}

// InlineQueryExecutor is the Assistant-facing subset of the shared inline engine.
type InlineQueryExecutor interface {
	Prepare(string) (inlineservice.PreparedQuery, error)
	ExecuteWithPeerType(context.Context, core.TelegramServicer, int64, int64, string, string, tg.InlineQueryPeerTypeClass) error
	ExecutePreparedWithPeerType(context.Context, core.TelegramServicer, int64, int64, inlineservice.PreparedQuery, string, tg.InlineQueryPeerTypeClass) error
}

func RegisterUpdateHandlers(dispatcher *tg.UpdateDispatcher, deps UpdateHandlerDeps) {
	if dispatcher == nil {
		return
	}
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
		if deps.IsShuttingDown != nil && deps.IsShuttingDown() {
			return nil
		}
		if deps.CacheEntities != nil {
			deps.CacheEntities(e)
		}
		msg, ok := update.Message.(*tg.Message)
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
		if deps.Resolver == nil || deps.Interaction == nil {
			return nil
		}
		inputPeer, err := deps.Resolver.Resolve(ctx, msg.PeerID, senderID, e)
		if err != nil || inputPeer == nil {
			logger.Warn("assistant: sender access hash missing, message ignored", zap.Int64("sender_id", senderID), zap.Error(err))
			return nil
		}
		if deps.InteractionIngress != nil {
			if handled, hErr := deps.InteractionIngress.tryText(ctx, msg.Message, senderID, extractChatID(msg.PeerID), inputPeer); handled {
				if hErr != nil {
					logger.Warn("assistant: interaction text input dispatch failed", zap.Error(hErr), zap.Int64("sender_id", senderID))
					if feedback := interactionTextInputErrorMessage(hErr); feedback != "" {
						_, _ = deps.Interaction.SendMessage(ctx, inputPeer, feedback, nil)
					}
				}
				return nil
			}
		}
		if deps.RateLimiter != nil && !deps.RateLimiter.Allow(senderID, "command") {
			logger.Warn("assistant: rate limit exceeded for command", zap.Int64("sender_id", senderID))
			return nil
		}
		if deps.CmdRouter == nil {
			return nil
		}
		err = deps.CmdRouter.Dispatch(ctx, senderID, inputPeer, msg.Message, deps.Interaction)
		if err != nil && !errors.Is(err, command.ErrUnknownCommand) {
			logger.Warn("assistant: command error", zap.Error(err), zap.Int64("sender_id", senderID))
		}
		return nil
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
