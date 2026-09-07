package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"github.com/inipew/goultroid/internal/settings"
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
		if deps.MenuController != nil && deps.SettingsService != nil {
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
			return nil
		}
		if deps.CacheEntities != nil {
			deps.CacheEntities(e)
		}
		if deps.RateLimiter != nil && !deps.RateLimiter.Allow(update.UserID, "inline") {
			logger.Warn("assistant: rate limit exceeded for inline", zap.Int64("user_id", update.UserID))
			return nil
		}
		return nil
	})
	dispatcher.OnInlineBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateInlineBotCallbackQuery) error {
		if deps.IsShuttingDown != nil && deps.IsShuttingDown() {
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
		payload, parseErr := callback.Parse(update.Data)
		if parseErr != nil {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Invalid callback", false)
			}
			return nil
		}
		if deps.CallbackRouter != nil && deps.Interaction != nil {
			tx := callback.NewInlineTransaction(update.QueryID, update.UserID, *payload, inlineTarget, deps.Interaction.AsInline())
			if err := dispatchInlineSafely(ctx, deps.CallbackRouter, tx, logger); err != nil {
				logger.Warn("assistant: inline callback router error", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID), zap.String("action", payload.Action))
			}
		}
		return nil
	})
	dispatcher.OnBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
		if deps.IsShuttingDown != nil && deps.IsShuttingDown() {
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
		payload, parseErr := callback.Parse(update.Data)
		if parseErr != nil {
			return nil
		}
		if deps.CallbackRouter != nil && deps.Interaction != nil {
			tx := callback.NewTransaction(update.QueryID, update.UserID, *payload, target, deps.Interaction)
			if err := dispatchCallbackSafely(ctx, deps.CallbackRouter, tx, logger); err != nil {
				logger.Warn("assistant: callback router error", zap.Error(err), zap.Int64("query_id", update.QueryID), zap.Int64("user_id", update.UserID), zap.String("action", payload.Action))
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
