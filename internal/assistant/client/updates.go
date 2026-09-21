package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type UpdateHandlerDeps struct {
	Logger          *zap.Logger
	RateLimiter     RateLimiter
	Resolver        peer.Resolver
	CmdRouter       *command.Router
	CallbackRouter  *callback.Router
	Interaction     *interaction.ClientInteraction
	CacheEntities   func(e tg.Entities)
	IsShuttingDown  func() bool
	MenuController  *menu.Controller
	SettingsService *settings.Service
	InlineEngine    InlineQueryExecutor
	InlineService   core.TelegramServicer
	Tasks           tasks.Client
	V2Ingress       *v2Ingress
}

// InlineQueryExecutor is the Assistant-facing subset of the shared inline engine.
type InlineQueryExecutor interface {
	ExecuteWithPeerType(context.Context, core.TelegramServicer, int64, int64, string, string, tg.InlineQueryPeerTypeClass) error
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
		if deps.MenuController != nil {
			if handled, hErr := deps.MenuController.HandleTextMessage(ctx, senderID, extractChatID(msg.PeerID), msg.Message, deps.Interaction, deps.SettingsService); handled {
				return hErr
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
		_, err := deps.Tasks.Submit(ctx, tasks.WorkSpec{
			ID:               tasks.TaskID(fmt.Sprintf("asst:inline:%d", update.QueryID)),
			QuotaOwner:       tasks.OwnerID(fmt.Sprintf("telegram:user:%d", update.UserID)),
			Pool:             "interactive",
			Class:            tasks.PriorityInteractive,
			OrderingKey:      fmt.Sprintf("inline:%d", update.QueryID),
			ExecutionTimeout: 5 * time.Second,
			Handler: func(taskCtx context.Context) error {
				return deps.InlineEngine.ExecuteWithPeerType(taskCtx, deps.InlineService, update.QueryID, update.UserID, update.Query, update.Offset, update.PeerType)
			},
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
		if deps.RateLimiter != nil && !deps.RateLimiter.Allow(update.UserID, "inline") {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Too many requests. Please wait.", true)
			}
			return nil
		}
		inlineTarget := interaction.NewInlineTarget(update.QueryID, update.MsgID, update.ChatInstance)
		if isV2Callback(update.Data) {
			if deps.V2Ingress == nil {
				if deps.Interaction != nil {
					_ = deps.Interaction.Answer(ctx, update.QueryID, "Interaction service unavailable.", false)
				}
				return nil
			}
			_, err := deps.V2Ingress.tryInline(ctx, update.Data, update.UserID, update.QueryID, update.MsgID)
			if err != nil {
				logger.Warn("assistant: a2 inline callback dispatch failed", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID))
			}
			return nil
		}
		payload, parseErr := callback.Parse(update.Data)
		if parseErr != nil {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Invalid callback", false)
			}
			return nil
		}
		if deps.CallbackRouter == nil {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Interaction service unavailable.", false)
			}
			return nil
		}
		if deps.Interaction != nil {
			tx := callback.NewInlineTransaction(update.QueryID, update.UserID, *payload, inlineTarget, deps.Interaction.AsInline())
			tx.RawData = update.Data
			if err := dispatchInlineSafely(ctx, deps.CallbackRouter, tx, logger); err != nil {
				logger.Warn("assistant: inline callback router error", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID), zap.String("action", payload.Action))
				if !tx.IsAnswered() {
					_ = tx.Answer(ctx, "Action failed. Please retry.", false)
				}
			}
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
		if deps.RateLimiter != nil && !deps.RateLimiter.Allow(update.UserID, "callback") {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Too many requests. Please wait.", true)
			}
			return nil
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
		if isV2Callback(update.Data) {
			if deps.V2Ingress == nil {
				if deps.Interaction != nil {
					_ = deps.Interaction.Answer(ctx, update.QueryID, "Interaction service unavailable.", false)
				}
				return nil
			}
			_, err := deps.V2Ingress.tryMessage(ctx, update.Data, update.UserID, update.QueryID, inputPeer, extractChatID(update.Peer), update.MsgID)
			if err != nil {
				logger.Warn("assistant: a2 callback dispatch failed", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID))
			}
			return nil
		}
		payload, parseErr := callback.Parse(update.Data)
		if parseErr != nil {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Invalid callback", false)
			}
			return nil
		}
		if deps.CallbackRouter == nil {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Interaction service unavailable.", false)
			}
			return nil
		}
		if deps.Interaction != nil {
			tx := callback.NewTransaction(update.QueryID, update.UserID, *payload, target, deps.Interaction)
			tx.RawData = update.Data
			if err := dispatchCallbackSafely(ctx, deps.CallbackRouter, tx, logger); err != nil {
				logger.Warn("assistant: callback router error", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID), zap.String("action", payload.Action))
				if !tx.IsAnswered() {
					_ = tx.Answer(ctx, "Action failed. Please retry.", false)
				}
			}
		}
		return nil
	})
}

func dispatchCallbackSafely(ctx context.Context, router *callback.Router, tx *callback.Transaction, logger *zap.Logger) (err error) {
	if router == nil || tx == nil {
		return callback.ErrUnknownAction
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			tx.SetState(callback.StateFailed)
			err = fmt.Errorf("callback handler panic: %v", recovered)
			if !tx.IsAnswered() {
				_ = tx.Answer(ctx, "Internal server error.", true)
			}
			if logger != nil {
				logger.Error("assistant: callback panic recovered", zap.Int64("query_id", tx.QueryID), zap.String("namespace", tx.Payload.Namespace), zap.String("action", tx.Payload.Action), zap.Any("panic", recovered))
			}
		}
	}()
	return router.Dispatch(ctx, tx)
}

func dispatchInlineSafely(ctx context.Context, router *callback.Router, tx *callback.InlineTransaction, logger *zap.Logger) (err error) {
	if router == nil || tx == nil {
		return callback.ErrUnknownAction
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			tx.SetState(callback.StateFailed)
			err = fmt.Errorf("inline callback handler panic: %v", recovered)
			if !tx.IsAnswered() {
				_ = tx.Answer(ctx, "Internal server error.", true)
			}
			if logger != nil {
				logger.Error("assistant: inline callback panic recovered", zap.Int64("query_id", tx.QueryID), zap.String("namespace", tx.Payload.Namespace), zap.String("action", tx.Payload.Action), zap.Any("panic", recovered))
			}
		}
	}()
	return router.DispatchInline(ctx, tx)
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
