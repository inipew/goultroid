package client

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
)

// CoreCallbackDispatcher routes callback query events to core and plugin handlers.
type CoreCallbackDispatcher interface {
	HasHandler(namespace string) bool
	Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error
}

// assistantCallbackServicer adapts an assistant Transaction into a core.TelegramServicer
// so delegated plugin callback handlers can edit messages, reply, and acknowledge queries.
type assistantCallbackServicer struct {
	core.MockTelegramServicer
	tx *callback.Transaction
}

func (s *assistantCallbackServicer) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	if s.tx != nil {
		return s.tx.Answer(ctx, text, alert)
	}
	return nil
}

func (s *assistantCallbackServicer) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if s.tx != nil && s.tx.Interaction != nil {
		target := s.tx.Target
		if peer != nil || msgID > 0 {
			chatID := target.ChatID()
			if chatID == 0 && peer != nil {
				chatID = extractChatIDFromInputPeer(peer)
			}
			target = interaction.NewMessageTarget(peer, msgID, chatID, target.ChatInstance())
		}
		return s.tx.Interaction.Edit(ctx, target, text, markup)
	}
	return nil
}

func (s *assistantCallbackServicer) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	if s.tx != nil && s.tx.Interaction != nil {
		target := s.tx.Target
		if peer != nil || msgID > 0 {
			chatID := target.ChatID()
			if chatID == 0 && peer != nil {
				chatID = extractChatIDFromInputPeer(peer)
			}
			target = interaction.NewMessageTarget(peer, msgID, chatID, target.ChatInstance())
		}
		return s.tx.Interaction.EditMarkup(ctx, target, markup)
	}
	return nil
}

func (s *assistantCallbackServicer) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return s.EditMessageMarkup(ctx, peer, msgID, text, nil)
}

func (s *assistantCallbackServicer) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if s.tx != nil {
		return s.tx.Delete(ctx)
	}
	return nil
}

func (s *assistantCallbackServicer) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if s.tx != nil && s.tx.Interaction != nil {
		target := s.tx.Target
		if peer != nil || msgID > 0 {
			chatID := target.ChatID()
			if chatID == 0 && peer != nil {
				chatID = extractChatIDFromInputPeer(peer)
			}
			target = interaction.NewMessageTarget(peer, msgID, chatID, target.ChatInstance())
		}
		return s.tx.Interaction.GetMessage(ctx, target)
	}
	return nil, nil
}

func (s *assistantCallbackServicer) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	return s.SendMessageWithMarkup(ctx, peer, text, nil)
}

func (s *assistantCallbackServicer) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if s.tx != nil && s.tx.Interaction != nil {
		if peer == nil {
			peer = s.tx.Target.Peer()
		}
		return s.tx.Interaction.SendMessage(ctx, peer, text, markup)
	}
	return nil, nil
}

// assistantInlineCallbackServicer adapts an assistant InlineTransaction into a core.TelegramServicer.
type assistantInlineCallbackServicer struct {
	core.MockTelegramServicer
	tx *callback.InlineTransaction
}

func (s *assistantInlineCallbackServicer) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	if s.tx != nil {
		return s.tx.Answer(ctx, text, alert)
	}
	return nil
}

func (s *assistantInlineCallbackServicer) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	if s.tx != nil && s.tx.Interaction != nil {
		target := s.tx.Target
		if inlineID != nil {
			target = interaction.NewInlineTarget(target.QueryID(), inlineID, target.ChatInstance())
		}
		return s.tx.Interaction.Edit(ctx, target, text, markup)
	}
	return nil
}

func (s *assistantInlineCallbackServicer) EditInlineBotMessageMarkup(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error {
	if s.tx != nil && s.tx.Interaction != nil {
		target := s.tx.Target
		if inlineID != nil {
			target = interaction.NewInlineTarget(target.QueryID(), inlineID, target.ChatInstance())
		}
		return s.tx.Interaction.Edit(ctx, target, "", markup)
	}
	return nil
}

func extractChatIDFromInputPeer(peer tg.InputPeerClass) int64 {
	if peer == nil {
		return 0
	}
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return p.UserID
	case *tg.InputPeerChat:
		return p.ChatID
	case *tg.InputPeerChannel:
		return p.ChannelID
	default:
		return 0
	}
}
