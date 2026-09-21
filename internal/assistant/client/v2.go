package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

var ErrV2Unavailable = errors.New("assistant/client: a2 interaction engine unavailable")

type callbackAcknowledger interface {
	ensureAnswered(context.Context, int64, error)
}

type v2Ingress struct {
	engine *orchestration.Engine
	ack    callbackAcknowledger
}

func isV2Callback(data []byte) bool {
	return strings.HasPrefix(string(data), "a2:")
}

func (v *v2Ingress) tryMessage(ctx context.Context, data []byte, userID, queryID int64, peer tg.InputPeerClass, chatID int64, msgID int) (bool, error) {
	if !isV2Callback(data) {
		return false, nil
	}
	if v == nil || v.engine == nil || v.ack == nil {
		return true, ErrV2Unavailable
	}
	target := presentationtelegram.MessageTarget{Peer: peer, ChatID: chatID, MessageID: msgID}
	err := v.engine.Dispatch(ctx, orchestration.CallbackRequest{
		Data: data, ActorID: userID, QueryID: queryID, Target: target,
	})
	v.ack.ensureAnswered(ctx, queryID, err)
	return true, err
}

func (v *v2Ingress) tryInline(ctx context.Context, data []byte, userID, queryID int64, messageID tg.InputBotInlineMessageIDClass) (bool, error) {
	if !isV2Callback(data) {
		return false, nil
	}
	if v == nil || v.engine == nil || v.ack == nil {
		return true, ErrV2Unavailable
	}
	target := presentationtelegram.InlineTarget{
		MessageID: messageID,
		BindingID: inlineBindingID(messageID),
	}
	err := v.engine.Dispatch(ctx, orchestration.CallbackRequest{
		Data: data, ActorID: userID, QueryID: queryID, Target: target,
	})
	v.ack.ensureAnswered(ctx, queryID, err)
	return true, err
}

func inlineBindingID(messageID tg.InputBotInlineMessageIDClass) string {
	if messageID == nil {
		return ""
	}
	return fmt.Sprintf("%T:%v", messageID, messageID)
}

type v2PresentationServicer struct {
	unsupportedTelegramServicer
	interaction *assistantinteraction.ClientInteraction

	mu       sync.Mutex
	answered map[int64]struct{}
}

func newV2PresentationServicer(interaction *assistantinteraction.ClientInteraction) *v2PresentationServicer {
	return &v2PresentationServicer{
		interaction: interaction,
		answered:    make(map[int64]struct{}),
	}
}

func (s *v2PresentationServicer) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if s == nil || s.interaction == nil {
		return nil, ErrV2Unavailable
	}
	return s.interaction.SendMessage(ctx, peer, text, markup)
}

func (s *v2PresentationServicer) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if s == nil || s.interaction == nil {
		return ErrV2Unavailable
	}
	target := assistantinteraction.NewMessageTarget(peer, msgID, extractChatIDFromInputPeer(peer), 0)
	return s.interaction.Edit(ctx, target, text, markup)
}

func (s *v2PresentationServicer) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	if s == nil || s.interaction == nil {
		return ErrV2Unavailable
	}
	target := assistantinteraction.NewInlineTarget(1, inlineID, 0)
	return s.interaction.AsInline().Edit(ctx, target, text, markup)
}

func (s *v2PresentationServicer) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	if s == nil || s.interaction == nil || queryID == 0 {
		return ErrV2Unavailable
	}
	s.mu.Lock()
	if _, answered := s.answered[queryID]; answered {
		s.mu.Unlock()
		return nil
	}
	s.answered[queryID] = struct{}{}
	s.mu.Unlock()
	if err := s.interaction.Answer(ctx, queryID, text, alert); err != nil {
		s.mu.Lock()
		delete(s.answered, queryID)
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *v2PresentationServicer) ensureAnswered(ctx context.Context, queryID int64, dispatchErr error) {
	if s == nil || s.interaction == nil || queryID == 0 {
		return
	}
	s.mu.Lock()
	_, answered := s.answered[queryID]
	delete(s.answered, queryID)
	s.mu.Unlock()
	if answered {
		return
	}
	text := ""
	if dispatchErr != nil {
		switch {
		case errors.Is(dispatchErr, rootinteraction.ErrExpired),
			errors.Is(dispatchErr, rootinteraction.ErrNotFound),
			errors.Is(dispatchErr, rootinteraction.ErrStaleToken),
			errors.Is(dispatchErr, rootinteraction.ErrScopeStale):
			text = "Interaction expired. Please reopen it."
		default:
			text = "Action failed. Please retry."
		}
	}
	_ = s.interaction.Answer(ctx, queryID, text, false)
}
