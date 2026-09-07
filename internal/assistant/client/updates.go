package client

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/peer"
	"go.uber.org/zap"
)

// UpdateHandlerDeps bundles all dependencies required to dispatch incoming MTProto updates.
type UpdateHandlerDeps struct {
	Logger         *zap.Logger
	RateLimiter    RateLimiter
	Resolver       peer.Resolver
	CmdRouter      *command.Router
	CallbackRouter *callback.Router
	Interaction    *interaction.ClientInteraction
	CacheEntities  func(e tg.Entities)
}

// RegisterUpdateHandlers registers message and callback handlers onto gotd's UpdateDispatcher.
func RegisterUpdateHandlers(dispatcher *tg.UpdateDispatcher, deps UpdateHandlerDeps) {
	if dispatcher == nil {
		return
	}
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	// 1. Message Dispatcher -> Command Router
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewMessage) error {
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

		if deps.RateLimiter != nil && !deps.RateLimiter.Allow(senderID, "command") {
			logger.Warn("assistant: rate limit exceeded for command", zap.Int64("sender_id", senderID))
			return nil
		}

		if deps.Resolver == nil || deps.CmdRouter == nil || deps.Interaction == nil {
			return nil
		}

		inputPeer, err := deps.Resolver.Resolve(ctx, msg.PeerID, senderID, e)
		if err != nil || inputPeer == nil {
			logger.Warn("assistant: sender access hash missing, command ignored", zap.Int64("sender_id", senderID), zap.Error(err))
			return nil
		}

		err = deps.CmdRouter.Dispatch(ctx, senderID, inputPeer, msg.Message, deps.Interaction)
		if err != nil && !errors.Is(err, command.ErrUnknownCommand) {
			logger.Warn("assistant: command error", zap.Error(err), zap.Int64("sender_id", senderID))
		}
		return nil
	})

	// 2a. Inline Query rate limiting (observe only, no handler yet — structural separation)
	dispatcher.OnBotInlineQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotInlineQuery) error {
		if deps.CacheEntities != nil {
			deps.CacheEntities(e)
		}
		if deps.RateLimiter != nil && !deps.RateLimiter.Allow(update.UserID, "inline") {
			logger.Warn("assistant: rate limit exceeded for inline", zap.Int64("user_id", update.UserID))
			return nil
		}
		// Inline query handling is delegated to separate inline engine (future: assistant/inline/*)
		return nil
	})

	dispatcher.OnInlineBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateInlineBotCallbackQuery) error {
		if deps.CacheEntities != nil {
			deps.CacheEntities(e)
		}
		if deps.RateLimiter != nil && !deps.RateLimiter.Allow(update.UserID, "callback") {
			if deps.Interaction != nil {
				_ = deps.Interaction.Answer(ctx, update.QueryID, "Too many requests. Please wait.", true)
			}
			return nil
		}
		// Inline callback uses InlineTarget — structurally separated from MessageTarget (§4)
		inlineTarget := interaction.NewInlineTarget(update.QueryID, update.MsgID, update.ChatInstance)
		_ = inlineTarget
		logger.Debug("assistant: inline callback received (structural separation)", zap.Int64("query_id", update.QueryID))
		// Inline edit via interaction.AsInline().Edit(...) when needed
		return nil
	})

	// 2. Callback Query Dispatcher -> Callback Router
	dispatcher.OnBotCallbackQuery(func(ctx context.Context, e tg.Entities, update *tg.UpdateBotCallbackQuery) error {
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
			inputPeer, _ = deps.Resolver.Resolve(ctx, update.Peer, update.UserID, e)
		}

		chatID := extractChatID(update.Peer)
		target := interaction.NewMessageTarget(inputPeer, update.MsgID, chatID, update.ChatInstance)

		payload, parseErr := callback.Parse(update.Data)
		if parseErr != nil {
			logger.Debug("assistant: non-v2 callback payload received", zap.ByteString("data", update.Data), zap.Error(parseErr))
			return nil
		}

		if deps.CallbackRouter != nil && deps.Interaction != nil {
			tx := callback.NewTransaction(update.QueryID, update.UserID, *payload, target, deps.Interaction)
			if err := deps.CallbackRouter.Dispatch(ctx, tx); err != nil {
				logger.Warn("assistant: callback router error",
					zap.Error(err),
					zap.Int64("query_id", update.QueryID),
					zap.Int64("user_id", update.UserID),
					zap.String("action", payload.Action),
				)
			}
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
