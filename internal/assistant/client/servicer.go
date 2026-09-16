package client

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/tasks"
)

// CoreCallbackDispatcher routes callback query events to core and plugin handlers.
type CoreCallbackDispatcher interface {
	HasHandler(namespace string) bool
	TaskScope(data []byte, resolve func(string) (tasks.ScopeIdentity, bool)) (tasks.ScopeIdentity, bool)
	Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error
}

func resolveMessageTarget(base interaction.MessageTarget, peer tg.InputPeerClass, msgID int) interaction.MessageTarget {
	tPeer := base.Peer()
	if peer != nil {
		tPeer = peer
	}
	tMsgID := base.MessageID()
	if msgID > 0 {
		tMsgID = msgID
	}
	chatID := base.ChatID()
	if peer != nil {
		if cID := extractChatIDFromInputPeer(peer); cID != 0 {
			chatID = cID
		}
	}
	return interaction.NewMessageTarget(tPeer, tMsgID, chatID, base.ChatInstance())
}

// assistantCallbackServicer adapts an assistant Transaction into a core.TelegramServicer
// so delegated plugin callback handlers can edit messages, reply, and acknowledge queries.
type assistantCallbackServicer struct {
	core.MockTelegramServicer
	tx *callback.Transaction
}

func (s *assistantCallbackServicer) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	if s.tx == nil {
		return core.ErrInternal
	}
	return s.tx.Answer(ctx, text, alert)
}

func (s *assistantCallbackServicer) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if s.tx == nil || s.tx.Interaction == nil {
		return core.ErrInternal
	}
	target := resolveMessageTarget(s.tx.Target, peer, msgID)
	return s.tx.Interaction.Edit(ctx, target, text, markup)
}

func (s *assistantCallbackServicer) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	if s.tx == nil || s.tx.Interaction == nil {
		return core.ErrInternal
	}
	target := resolveMessageTarget(s.tx.Target, peer, msgID)
	return s.tx.Interaction.EditMarkup(ctx, target, markup)
}

func (s *assistantCallbackServicer) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return s.EditMessageMarkup(ctx, peer, msgID, text, nil)
}

func (s *assistantCallbackServicer) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if s.tx == nil || s.tx.Interaction == nil {
		return core.ErrInternal
	}
	if len(msgIDs) == 0 {
		target := resolveMessageTarget(s.tx.Target, peer, 0)
		return s.tx.Interaction.Delete(ctx, target)
	}
	var firstErr error
	for _, id := range msgIDs {
		target := resolveMessageTarget(s.tx.Target, peer, id)
		if err := s.tx.Interaction.Delete(ctx, target); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *assistantCallbackServicer) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if s.tx == nil || s.tx.Interaction == nil {
		return nil, core.ErrInternal
	}
	target := resolveMessageTarget(s.tx.Target, peer, msgID)
	return s.tx.Interaction.GetMessage(ctx, target)
}

func (s *assistantCallbackServicer) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	return s.SendMessageWithMarkup(ctx, peer, text, nil)
}

func (s *assistantCallbackServicer) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if s.tx == nil || s.tx.Interaction == nil {
		return nil, core.ErrInternal
	}
	if peer == nil {
		peer = s.tx.Target.Peer()
	}
	return s.tx.Interaction.SendMessage(ctx, peer, text, markup)
}

// assistantInlineCallbackServicer adapts an assistant InlineTransaction into a core.TelegramServicer.
type assistantInlineCallbackServicer struct {
	core.MockTelegramServicer
	tx *callback.InlineTransaction
}

func (s *assistantInlineCallbackServicer) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	if s.tx == nil {
		return core.ErrInternal
	}
	return s.tx.Answer(ctx, text, alert)
}

func (s *assistantInlineCallbackServicer) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	if s.tx == nil || s.tx.Interaction == nil {
		return core.ErrInternal
	}
	target := s.tx.Target
	if inlineID != nil {
		target = interaction.NewInlineTarget(target.QueryID(), inlineID, target.ChatInstance())
	}
	return s.tx.Interaction.Edit(ctx, target, text, markup)
}

func (s *assistantInlineCallbackServicer) EditInlineBotMessageMarkup(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error {
	if s.tx == nil || s.tx.Interaction == nil {
		return core.ErrInternal
	}
	target := s.tx.Target
	if inlineID != nil {
		target = interaction.NewInlineTarget(target.QueryID(), inlineID, target.ChatInstance())
	}
	return s.tx.Interaction.EditMarkup(ctx, target, markup)
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
